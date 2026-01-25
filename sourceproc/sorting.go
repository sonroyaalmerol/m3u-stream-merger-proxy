package sourceproc

import (
	"encoding/json"
	"fmt"
	"m3u-stream-merger/config"
	"m3u-stream-merger/logger"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/cespare/xxhash"
	"github.com/puzpuzpuz/xsync/v3"
)

const (
	mutexShards       = 4096
	shardFileTemplate = "shard-%04d.json"
)

type SortingManager struct {
	shardMap   *xsync.MapOf[uint64, *shardData]
	sortingKey string
	sortingDir string
	basePath   string
}

type shardData struct {
	index  map[string]bool
	buffer map[string][]byte
}

func newSortingManager() *SortingManager {
	sortingKey := os.Getenv("SORTING_KEY")
	sortingDir := strings.ToLower(os.Getenv("SORTING_DIRECTION"))
	basePath := config.GetSortDirPath()

	if err := os.MkdirAll(basePath, 0755); err != nil {
		logger.Default.Error(err.Error())
	}

	return &SortingManager{
		shardMap:   xsync.NewMapOf[uint64, *shardData](),
		sortingKey: sortingKey,
		sortingDir: sortingDir,
		basePath:   basePath,
	}
}

func (m *SortingManager) AddToSorter(s *StreamInfo) error {
	titleHash := xxhash.Sum64String(s.Title)
	shardIndex := titleHash % mutexShards
	sanitizedTitle := sanitizeField(s.Title)

	var addErr error
	m.shardMap.Compute(shardIndex, func(oldVal *shardData, loaded bool) (*shardData, bool) {
		if oldVal == nil {
			oldVal = &shardData{
				index:  make(map[string]bool),
				buffer: make(map[string][]byte),
			}
		}

		if oldVal.index[sanitizedTitle] {
			addErr = m.handleExisting(shardIndex, sanitizedTitle, s)
			return oldVal, false
		}

		shardFile := filepath.Join(m.basePath, fmt.Sprintf(shardFileTemplate, shardIndex))
		if _, err := os.Stat(shardFile); err == nil {
			entries, err := m.readShard(shardIndex)
			if err != nil {
				addErr = err
				return oldVal, false
			}
			for title := range entries {
				oldVal.index[title] = true
			}
			if oldVal.index[sanitizedTitle] {
				addErr = m.handleExisting(shardIndex, sanitizedTitle, s)
				return oldVal, false
			}
		}

		encoded, err := json.Marshal(s)
		if err != nil {
			addErr = fmt.Errorf("failed to marshal StreamInfo: %w", err)
			return oldVal, false
		}

		oldVal.buffer[sanitizedTitle] = encoded
		oldVal.index[sanitizedTitle] = true

		if len(oldVal.buffer) >= 250 {
			if err := m.flushShard(shardIndex, oldVal); err != nil {
				addErr = err
			}
		}

		return oldVal, false
	})

	return addErr
}

func (m *SortingManager) Close() {
	basePath := config.GetSortDirPath()
	os.RemoveAll(basePath)
}

func (m *SortingManager) handleExisting(shardIndex uint64, title string, s *StreamInfo) error {
	entries, err := m.readShard(shardIndex)
	if err != nil {
		return err
	}

	if existing, exists := entries[title]; exists {
		merged := mergeStreamInfoAttributes(existing, s)
		entries[title] = merged
	} else {
		entries[title] = s
	}

	return m.writeShard(shardIndex, entries)
}

func (m *SortingManager) flushShard(shardIndex uint64, data *shardData) error {
	if len(data.buffer) == 0 {
		return nil
	}

	entries, err := m.readShard(shardIndex)
	if err != nil {
		return err
	}

	for title, buf := range data.buffer {
		var s StreamInfo
		if err := json.Unmarshal(buf, &s); err != nil {
			continue
		}
		entries[title] = &s
	}

	if err := m.writeShard(shardIndex, entries); err != nil {
		return err
	}

	data.buffer = make(map[string][]byte)
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

func (m *SortingManager) GetSortedEntries(callback func(*StreamInfo)) error {
	for shardIndex := 0; shardIndex < mutexShards; shardIndex++ {
		index := uint64(shardIndex)
		m.shardMap.Compute(index, func(oldVal *shardData, loaded bool) (*shardData, bool) {
			if oldVal == nil {
				return nil, false
			}
			if err := m.flushShard(index, oldVal); err != nil {
				logger.Default.Errorf("failed to flush shard %d: %v", index, err)
			}
			return oldVal, false
		})
	}

	entries := make([]*StreamInfo, 0, 1_000_000)

	for shardIndex := 0; shardIndex < mutexShards; shardIndex++ {
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

		for _, stream := range shardData {
			entries = append(entries, stream)
		}
	}

	sort.Slice(entries, func(i, j int) bool {
		iKey := getSortKey(entries[i], m.sortingKey, m.sortingDir)
		jKey := getSortKey(entries[j], m.sortingKey, m.sortingDir)
		return iKey < jKey
	})

	for _, entry := range entries {
		callback(entry)
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
		base.URLs = xsync.NewMapOf[string, map[string]string]()
	}

	new.URLs.Range(func(key string, value map[string]string) bool {
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
