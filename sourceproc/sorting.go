package sourceproc

import (
	"encoding/json"
	"fmt"
	"m3u-stream-merger/config"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/utils/safemap"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/cespare/xxhash"
)

const (
	mutexShards       = 4096
	shardFileTemplate = "shard-%04d.json"
)

type SortingManager struct {
	muxes      []*sync.Mutex       // Sharded mutexes
	indexes    []map[string]bool   // In-memory existence checks
	buffers    []map[string][]byte // Buffered writes
	sortingKey string
	sortingDir string
	basePath   string
}

func newSortingManager() *SortingManager {
	sortingKey := os.Getenv("SORTING_KEY")
	sortingDir := strings.ToLower(os.Getenv("SORTING_DIRECTION"))
	basePath := config.GetSortDirPath()

	if err := os.MkdirAll(basePath, 0755); err != nil {
		logger.Default.Error(err.Error())
	}

	muxes := make([]*sync.Mutex, mutexShards)
	indexes := make([]map[string]bool, mutexShards)
	buffers := make([]map[string][]byte, mutexShards)

	for i := range muxes {
		muxes[i] = &sync.Mutex{}
		indexes[i] = make(map[string]bool)
		buffers[i] = make(map[string][]byte)
	}

	return &SortingManager{
		muxes:      muxes,
		indexes:    indexes,
		buffers:    buffers,
		sortingKey: sortingKey,
		sortingDir: sortingDir,
		basePath:   basePath,
	}
}

func (m *SortingManager) AddToSorter(s *StreamInfo) error {
	titleHash := xxhash.Sum64String(s.Title)
	shardIndex := titleHash % mutexShards
	mutex := m.muxes[shardIndex]
	sanitizedTitle := sanitizeField(s.Title)

	mutex.Lock()
	defer mutex.Unlock()

	// Check in-memory index first
	if m.indexes[shardIndex][sanitizedTitle] {
		return m.handleExisting(shardIndex, sanitizedTitle, s)
	}

	// Check filesystem if not in memory index
	shardFile := filepath.Join(m.basePath, fmt.Sprintf(shardFileTemplate, shardIndex))
	if _, err := os.Stat(shardFile); err == nil {
		if err := m.loadShard(shardIndex); err != nil {
			return err
		}
		if m.indexes[shardIndex][sanitizedTitle] {
			return m.handleExisting(shardIndex, sanitizedTitle, s)
		}
	}

	// New entry - buffer in memory
	encoded, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("failed to marshal StreamInfo: %w", err)
	}

	m.buffers[shardIndex][sanitizedTitle] = encoded
	m.indexes[shardIndex][sanitizedTitle] = true

	// Flush buffer if reaching memory limit
	if len(m.buffers[shardIndex]) >= 250 { // ~1MB per shard buffer
		return m.flushShard(shardIndex)
	}

	return nil
}

func (m *SortingManager) Close() {
	basePath := config.GetSortDirPath()
	os.RemoveAll(basePath)
}

func (m *SortingManager) handleExisting(shardIndex uint64, title string, s *StreamInfo) error {
	// Read existing entries
	entries, err := m.readShard(shardIndex)
	if err != nil {
		return err
	}

	// Find and merge existing entry
	if existing, exists := entries[title]; exists {
		merged := mergeStreamInfoAttributes(existing, s)
		entries[title] = merged
	} else {
		entries[title] = s
	}

	// Write back merged entries
	return m.writeShard(shardIndex, entries)
}

func (m *SortingManager) flushShard(shardIndex uint64) error {
	if len(m.buffers[shardIndex]) == 0 {
		return nil
	}

	// Read existing entries if any
	entries, _ := m.readShard(shardIndex)

	// Merge buffered entries
	for title, data := range m.buffers[shardIndex] {
		var s StreamInfo
		if err := json.Unmarshal(data, &s); err != nil {
			continue
		}
		entries[title] = &s
	}

	// Write merged entries
	if err := m.writeShard(shardIndex, entries); err != nil {
		return err
	}

	// Clear buffer
	m.buffers[shardIndex] = make(map[string][]byte)
	return nil
}

func (m *SortingManager) readShard(shardIndex uint64) (map[string]*StreamInfo, error) {
	shardFile := filepath.Join(m.basePath, fmt.Sprintf(shardFileTemplate, shardIndex))
	entries := make(map[string]*StreamInfo)

	file, err := os.Open(shardFile)
	if err != nil {
		if os.IsNotExist(err) {
			return entries, nil
		}
		return nil, err
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&entries); err != nil {
		return nil, fmt.Errorf("failed to decode shard: %w", err)
	}

	return entries, nil
}

func (m *SortingManager) writeShard(shardIndex uint64, entries map[string]*StreamInfo) error {
	shardFile := filepath.Join(m.basePath, fmt.Sprintf(shardFileTemplate, shardIndex))

	file, err := os.Create(shardFile)
	if err != nil {
		return fmt.Errorf("failed to create shard file: %w", err)
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	if err := encoder.Encode(entries); err != nil {
		return fmt.Errorf("failed to encode shard: %w", err)
	}

	return nil
}

func (m *SortingManager) loadShard(shardIndex uint64) error {
	entries, err := m.readShard(shardIndex)
	if err != nil {
		return err
	}

	for title := range entries {
		m.indexes[shardIndex][title] = true
	}
	return nil
}

func (m *SortingManager) GetSortedEntries(callback func(*StreamInfo)) error {
	// First flush any buffered entries
	for shardIndex := uint64(0); shardIndex < mutexShards; shardIndex++ {
		m.muxes[shardIndex].Lock()
		if err := m.flushShard(shardIndex); err != nil {
			m.muxes[shardIndex].Unlock()
			return fmt.Errorf("failed to flush shard %d: %w", shardIndex, err)
		}
		m.muxes[shardIndex].Unlock()
	}

	// Collect all entries from all shards
	type shardEntry struct {
		key   string
		value *StreamInfo
	}
	entries := make([]shardEntry, 0, 1_000_000) // Preallocate for 1M entries

	// Read and parse all shard files
	for shardIndex := uint64(0); shardIndex < mutexShards; shardIndex++ {
		shardFile := filepath.Join(m.basePath, fmt.Sprintf(shardFileTemplate, shardIndex))

		file, err := os.Open(shardFile)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("failed to open shard %d: %w", shardIndex, err)
		}

		var shardData map[string]*StreamInfo
		if err := json.NewDecoder(file).Decode(&shardData); err != nil {
			file.Close()
			return fmt.Errorf("failed to decode shard %d: %w", shardIndex, err)
		}
		file.Close()

		// Convert map to sortable slice
		for _, stream := range shardData {
			entries = append(entries, shardEntry{
				key:   getSortKey(stream, m.sortingKey, m.sortingDir),
				value: stream,
			})
		}
	}

	// Sort the entries
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].key < entries[j].key
	})

	// Execute callback in sorted order
	for _, entry := range entries {
		callback(entry.value)
	}

	return nil
}

// Helper to replicate the filename-based sorting logic
func getSortKey(s *StreamInfo, sortingKey, direction string) string {
	switch sortingKey {
	case "tvg-id":
		return normalizeNumericField(s.TvgID, 10, direction)
	case "tvg-chno", "channel-id", "channel-number":
		return normalizeNumericField(s.TvgChNo, 10, direction)
	case "tvg-group", "group-title":
		return normalizeStringField(s.Group, direction)
	case "tvg-type":
		return normalizeStringField(s.TvgType, direction)
	case "source":
		return normalizeNumericField(s.SourceM3U, 5, direction)
	default: // Title
		return normalizeStringField(s.Title, direction)
	}
}

func mergeStreamInfoAttributes(base, new *StreamInfo) *StreamInfo {
	if base.Title == "" {
		base.Title = new.Title
	}
	if base.TvgID == "" {
		base.TvgID = new.TvgID
	}
	if base.TvgChNo == "" {
		base.TvgChNo = new.TvgChNo
	}
	if base.TvgType == "" {
		base.TvgType = new.TvgType
	}
	if base.LogoURL == "" {
		base.LogoURL = new.LogoURL
	}
	if base.Group == "" {
		base.Group = new.Group
	}

	if base.URLs == nil {
		base.URLs = safemap.New[string, map[string]string]()
	}

	new.URLs.ForEach(func(key string, value map[string]string) bool {
		_, _ = base.URLs.Compute(key, func(oldValue map[string]string, loaded bool) (newValue map[string]string, del bool) {
			if oldValue == nil {
				oldValue = value
			} else {
				for subKey, subValue := range value {
					oldValue[subKey] = subValue
				}
			}

			return oldValue, false
		})

		return true
	})

	if new.SourceM3U < base.SourceM3U || (new.SourceM3U == base.SourceM3U && new.SourceIndex < base.SourceIndex) {
		base.SourceM3U = new.SourceM3U
		base.SourceIndex = new.SourceIndex
	}

	return base
}

func normalizeNumericField(value string, width int, direction string) string {
	num, err := strconv.Atoi(value)
	if err != nil {
		return sanitizeField(value)
	}
	if direction == "desc" {
		maxValue := int64(1<<31 - 1) // Use a large constant (e.g., max int32)
		return fmt.Sprintf("%0*d", width, maxValue-int64(num))
	}
	return fmt.Sprintf("%0*d", width, num)
}

func normalizeStringField(value, direction string) string {
	if direction == "desc" {
		return reverseLexicographical(value)
	}
	return sanitizeField(value)
}

func reverseLexicographical(value string) string {
	return fmt.Sprintf("~%s", value)
}

func sanitizeField(value string) string {
	santized := strings.NewReplacer(
		"/", "_",
		"\\", "_",
		":", "_",
		"*", "_",
		"?", "_",
		"\"", "_",
		"<", "_",
		">", "_",
		"|", "_",
		" ", "",
	).Replace(value)

	if len(santized) > 100 {
		santized = santized[:100]
	}

	return santized
}
