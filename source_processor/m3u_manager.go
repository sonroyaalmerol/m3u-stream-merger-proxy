package sourceproc

import (
	"bufio"
	"container/heap"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"m3u-stream-merger/config"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/utils"
)

var sortingKey string
var sortingDir string

// M3UEntry represents a single stream entry
type M3UEntry struct {
	content    string
	streamInfo *StreamInfo
	index      int
}

// M3UHeap implements heap.Interface
// M3UHeap implements heap.Interface
type M3UHeap []*M3UEntry

func (h M3UHeap) Len() int { return len(h) }

func (h M3UHeap) Less(i, j int) bool {
	return getSortFunction(sortingKey, sortingDir)(h[i].streamInfo, h[j].streamInfo, "", "")
}

func (h M3UHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	// Update each entry’s index
	h[i].index = i
	h[j].index = j
}

func (h *M3UHeap) Push(x interface{}) {
	entry := x.(*M3UEntry)
	// Set the new element's index to the new length before appending.
	entry.index = len(*h)
	*h = append(*h, entry)
}

func (h *M3UHeap) Pop() interface{} {
	old := *h
	n := len(old)
	entry := old[n-1]
	// Invalidate the index of the popped element.
	entry.index = -1
	*h = old[0 : n-1]
	return entry
}

// M3UManager manages stream entries with sorting
type M3UManager struct {
	sync.RWMutex
	baseURL          string
	processedStreams *ShardedStreamMap
	streamCount      atomic.Int64
	isProcessing     atomic.Bool
	file             *os.File
	writer           *bufio.Writer
	streamHeap       M3UHeap
	heapEntries      map[string]*M3UEntry
	initialDone      chan struct{}
	initialOnce      sync.Once
}

func NewM3UManager(r *http.Request) *M3UManager {
	tmpPath := filepath.Join(config.GetConfig().TempPath, "tmp.m3u")
	file, err := createCacheFile(tmpPath)
	if err != nil {
		logger.Default.Errorf("Error creating cache file: %v", err)
		return nil
	}

	sortingKey = os.Getenv("SORTING_KEY")
	sortingDir = strings.ToLower(os.Getenv("SORTING_DIRECTION"))

	cache := &M3UManager{
		processedStreams: newShardedStreamMap(),
		baseURL:          utils.DetermineBaseURL(r),
		file:             file,
		writer:           bufio.NewWriter(file),
		streamHeap:       make(M3UHeap, 0),
		heapEntries:      make(map[string]*M3UEntry),
		initialDone:      make(chan struct{}),
	}
	heap.Init(&cache.streamHeap)

	// Write initial M3U header
	_, _ = cache.writer.WriteString("#EXTM3U\n")
	cache.writer.Flush()

	return cache
}

func (sc *M3UManager) processM3UsInRealTime() chan string {
	updates := make(chan string, 100)
	streamResults := streamDownloadM3USources()
	streamCh := make(chan *StreamInfo, 100)

	go func() {
		defer close(updates)
		defer sc.cleanup()

		sc.isProcessing.Store(true)
		defer sc.isProcessing.Store(false)

		var wg sync.WaitGroup
		// Process incoming streams
		for result := range streamResults {
			wg.Add(1)
			go func(res *SourceProcessorResult) {
				defer wg.Done()
				sc.handleSourceProcessorResult(res, streamCh)
			}(result)
		}

		// Wait for all processors in separate goroutine
		go func() {
			wg.Wait()
			close(streamCh)
		}()

		// Process streams and send updates
		for stream := range streamCh {
			if entry := sc.addStreamToCache(stream); entry != "" {
				updates <- entry
			}
		}

		// Final write when all streams are processed
		sc.finalize()
	}()

	return updates
}

func (sc *M3UManager) addStreamToCache(stream *StreamInfo) string {
	if stream == nil || len(stream.URLs) == 0 {
		return ""
	}

	sc.Lock()
	defer sc.Unlock()

	shard := sc.processedStreams.getShard(stream.Title)
	shard.Lock()
	defer shard.Unlock()

	// Check if a stream with the same title already exists.
	if existing, exists := shard.streams[stream.Title]; exists {
		// Merge new data into the existing stream.
		mergeURLs(existing, stream)
		mergeAttributes(existing, stream)
		// If we have a corresponding heap entry, update its content and reheapify.
		if heapEntry, ok := sc.heapEntries[stream.Title]; ok {
			heapEntry.content = formatStreamEntry(sc.baseURL, existing)
			heap.Fix(&sc.streamHeap, heapEntry.index)
		}
		return ""
	}

	// Otherwise, add the new stream.
	shard.streams[stream.Title] = stream
	sc.streamCount.Add(1)

	entryStr := formatStreamEntry(sc.baseURL, stream)
	newEntry := &M3UEntry{
		content:    entryStr,
		streamInfo: stream,
		// The index will be set inside Push.
	}

	// Push the new entry onto the heap.
	heap.Push(&sc.streamHeap, newEntry)
	// Save the pointer to this entry in the mapping.
	sc.heapEntries[stream.Title] = newEntry

	return entryStr
}

func (sc *M3UManager) finalize() {
	sc.Lock()
	defer sc.Unlock()

	// Truncate file and seek to start
	_ = sc.file.Truncate(0)
	_, _ = sc.file.Seek(0, 0)

	// Write single header
	_, _ = sc.writer.WriteString("#EXTM3U\n")

	// Write sorted entries; reformat each entry with current attributes.
	for sc.streamHeap.Len() > 0 {
		entry := heap.Pop(&sc.streamHeap).(*M3UEntry)
		// Reformat the entry using the latest stream info.
		updatedEntry := formatStreamEntry(sc.baseURL, entry.streamInfo)
		_, _ = sc.writer.WriteString(updatedEntry)
	}

	// Flush and move file
	sc.writer.Flush()

	tmpPath := sc.file.Name()
	finalPath := config.GetM3UCachePath()

	sc.file.Close()

	// First try a simple rename (fastest when possible)
	err := os.Rename(tmpPath, finalPath)
	if err != nil {
		if linkErr, ok := err.(*os.LinkError); ok &&
			linkErr.Err.Error() == "invalid cross-device link" {
			// Fall back to copy if it's a cross-device error
			if err := copyFileContentsFast(tmpPath, finalPath); err != nil {
				logger.Default.Errorf("Error copying cache file: %v", err)
				return
			}
			// Clean up temp file after successful copy
			os.Remove(tmpPath)
		} else {
			logger.Default.Errorf("Error moving cache file: %v", err)
			return
		}
	}

	logger.Default.Log("M3U cache has finished the revalidation process.")

	// Signal that the initial revalidation is complete.
	sc.initialOnce.Do(func() {
		close(sc.initialDone)
	})
}

// Optimized copy function with buffered I/O
func copyFileContentsFast(src, dst string) error {
	// Ensure destination directory exists
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	// Open source file
	source, err := os.Open(src)
	if err != nil {
		return err
	}
	defer source.Close()

	// Create destination file
	destination, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destination.Close()

	// Use a large buffer for better performance
	buf := make([]byte, 32*1024) // 32KB buffer
	_, err = io.CopyBuffer(destination, source, buf)
	if err != nil {
		return err
	}

	// Sync to ensure write is complete
	return destination.Sync()
}

func (sc *M3UManager) cleanup() {
	if sc.writer != nil {
		sc.writer.Flush()
	}
	if sc.file != nil {
		sc.file.Close()
	}
}

func (sc *M3UManager) GetCurrentContent() string {
	sc.RLock()
	defer sc.RUnlock()

	// Read current content from file
	content, err := os.ReadFile(sc.file.Name())
	if err != nil {
		logger.Default.Errorf("Error reading cache file: %v", err)
		return "#EXTM3U\n"
	}

	return string(content)
}

func (sc *M3UManager) IsProcessing() bool {
	return sc.isProcessing.Load()
}

func (sc *M3UManager) GetProcessedStreamsCount() int64 {
	return sc.streamCount.Load()
}

func (sc *M3UManager) handleSourceProcessorResult(result *SourceProcessorResult, streamCh chan<- *StreamInfo) {
	var currentLine string

	// Handle errors asynchronously
	go func() {
		for err := range result.Error {
			if err != nil {
				logger.Default.Errorf("Error processing M3U %s: %v", result.Index, err)
			}
		}
	}()

	// Process lines as they come in
	for lineInfo := range result.Lines {
		line := strings.TrimSpace(lineInfo.Content)
		if strings.HasPrefix(line, "#EXTINF:") {
			currentLine = line
		} else if currentLine != "" && !strings.HasPrefix(line, "#") {
			if streamInfo := parseLine(currentLine, lineInfo, result.Index); streamInfo != nil {
				if checkFilter(streamInfo) {
					streamCh <- streamInfo
				}
			}
			currentLine = ""
		}
	}
}

func createCacheFile(path string) (*os.File, error) {
	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}

	// Create file with buffered writes
	return os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
}
