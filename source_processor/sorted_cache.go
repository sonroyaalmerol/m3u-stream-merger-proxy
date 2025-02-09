package sourceproc

import (
	"bufio"
	"container/heap"
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

// M3UEntry represents a single stream entry
type M3UEntry struct {
	content    string
	streamInfo *StreamInfo
	index      int
}

// M3UHeap implements heap.Interface
type M3UHeap []*M3UEntry

func (h M3UHeap) Len() int { return len(h) }
func (h M3UHeap) Less(i, j int) bool {
	key := os.Getenv("SORTING_KEY")
	dir := strings.ToLower(os.Getenv("SORTING_DIRECTION"))
	return getSortFunction(key, dir)(h[i].streamInfo, h[j].streamInfo, "", "")
}
func (h M3UHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *M3UHeap) Push(x interface{}) { *h = append(*h, x.(*M3UEntry)) }
func (h *M3UHeap) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[0 : n-1]
	return x
}

// SortedM3UCache manages stream entries with sorting
type SortedM3UCache struct {
	sync.RWMutex
	baseURL          string
	processedStreams *ShardedStreamMap
	streamCount      atomic.Int64
	isProcessing     atomic.Bool
	file             *os.File
	writer           *bufio.Writer
	streamHeap       M3UHeap
	entryCount       int
}

func NewSortedM3UCache(r *http.Request) *SortedM3UCache {
	tmpPath := filepath.Join(config.GetConfig().TempPath, "tmp.m3u")
	file, err := createCacheFile(tmpPath)
	if err != nil {
		logger.Default.Errorf("Error creating cache file: %v", err)
		return nil
	}

	cache := &SortedM3UCache{
		processedStreams: newShardedStreamMap(),
		baseURL:          utils.DetermineBaseURL(r),
		file:             file,
		writer:           bufio.NewWriter(file),
		streamHeap:       make(M3UHeap, 0),
	}

	heap.Init(&cache.streamHeap)

	// Write initial M3U header
	cache.writer.WriteString("#EXTM3U\n")
	cache.writer.Flush()

	return cache
}

func (sc *SortedM3UCache) processM3UsInRealTime() chan string {
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

func (sc *SortedM3UCache) addStreamToCache(stream *StreamInfo) string {
	if stream == nil || len(stream.URLs) == 0 {
		return ""
	}

	sc.Lock()
	defer sc.Unlock()

	shard := sc.processedStreams.getShard(stream.Title)
	shard.Lock()
	defer shard.Unlock()

	// Check for existing stream
	if existing, exists := shard.streams[stream.Title]; exists {
		mergeURLs(existing, stream)
		return "" // No update needed for merged streams
	}

	// Add new stream
	shard.streams[stream.Title] = stream
	sc.streamCount.Add(1)

	// Format and add entry to heap
	entry := formatStreamEntry(sc.baseURL, stream)
	heap.Push(&sc.streamHeap, &M3UEntry{
		content:    entry,
		streamInfo: stream,
		index:      sc.entryCount,
	})
	sc.entryCount++

	return entry
}

func (sc *SortedM3UCache) finalize() {
	sc.Lock()
	defer sc.Unlock()

	// Truncate file and seek to start
	sc.file.Truncate(0)
	sc.file.Seek(0, 0)

	// Write single header
	sc.writer.WriteString("#EXTM3U\n")

	// Write sorted entries
	for sc.streamHeap.Len() > 0 {
		entry := heap.Pop(&sc.streamHeap).(*M3UEntry)
		sc.writer.WriteString(entry.content)
	}

	// Flush and move file
	sc.writer.Flush()

	tmpPath := sc.file.Name()
	finalPath := config.GetM3UCachePath()

	sc.file.Close()
	if err := os.Rename(tmpPath, finalPath); err != nil {
		logger.Default.Errorf("Error moving cache file: %v", err)
	}

	logger.Default.Log("M3U cache has finished the revalidation process.")
}

func (sc *SortedM3UCache) cleanup() {
	if sc.writer != nil {
		sc.writer.Flush()
	}
	if sc.file != nil {
		sc.file.Close()
	}
}

func (sc *SortedM3UCache) GetCurrentContent() string {
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

func (sc *SortedM3UCache) IsProcessing() bool {
	return sc.isProcessing.Load()
}

func (sc *SortedM3UCache) GetProcessedStreamsCount() int64 {
	return sc.streamCount.Load()
}

func (sc *SortedM3UCache) handleSourceProcessorResult(result *SourceProcessorResult, streamCh chan<- *StreamInfo) {
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
