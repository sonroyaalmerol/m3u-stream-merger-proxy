package sourceproc

import (
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"

	"m3u-stream-merger/config"
	"m3u-stream-merger/logger"
)

type M3UCache struct {
	cache atomic.Pointer[SortedM3UCache]
}

var M3uCache = &M3UCache{}

func RevalidatingGetM3U(r *http.Request, force bool) string {
	// Get current cache pointer atomically
	currentCache := M3uCache.cache.Load()

	// Check if we can use existing cache
	if !force && currentCache != nil {
		if !currentCache.IsProcessing() {
			return readCacheFromFile()
		}
		return currentCache.GetCurrentContent()
	}

	// Create new cache and start processing
	newCache := NewSortedM3UCache(r)
	if force {
		if err := os.Remove(config.GetM3UCachePath()); err != nil && !os.IsNotExist(err) {
			logger.Default.Errorf("Error removing existing cache: %v", err)
		}
		M3uCache.cache.Store(newCache)
	} else {
		if !M3uCache.cache.CompareAndSwap(nil, newCache) {
			// Another thread already created a cache, use that one
			return M3uCache.cache.Load().GetCurrentContent()
		}
	}

	// Initialize processing
	logger.Default.Log("Starting M3U cache processing...")
	go func() {
		processCount := 0
		updates := newCache.processM3UsInRealTime()
		for range updates {
			processCount++
			if processCount%100 == 0 {
				logger.Default.Logf("Processed %d streams so far", processCount)
			}
		}
		logger.Default.Logf("Completed processing %d total streams", processCount)
	}()

	return newCache.GetCurrentContent()
}

// GetCurrentStreams returns a map of all currently processed streams
func GetCurrentStreams() map[string]*StreamInfo {
	m3uCache := M3uCache.cache.Load()
	if m3uCache != nil {
		return m3uCache.processedStreams.toMap()
	}
	return make(map[string]*StreamInfo)
}

// GetProcessedStreamCount returns the number of processed streams
func GetProcessedStreamCount() int64 {
	m3uCache := M3uCache.cache.Load()
	if m3uCache != nil {
		return m3uCache.GetProcessedStreamsCount()
	}
	return 0
}

// IsProcessing returns whether the cache is currently processing streams
func IsProcessing() bool {
	m3uCache := M3uCache.cache.Load()
	if m3uCache != nil {
		return M3uCache.cache.Load().IsProcessing()
	}
	return false
}

// ClearCache clears both memory and disk cache
func ClearCache() {
	m3uCache := M3uCache.cache.Load()
	if m3uCache != nil {
		m3uCache.processedStreams.clear()
		M3uCache.cache.Store(nil)
	}

	logger.Default.Log("Clearing memory and disk M3U cache.")
	cleanupCacheFiles()
}

// Helper function to cleanup cache files
func cleanupCacheFiles() {
	// Remove main cache file
	if err := os.Remove(config.GetM3UCachePath()); err != nil && !os.IsNotExist(err) {
		logger.Default.Errorf("Cache file deletion failed: %v", err)
	}

	// Remove streams directory
	if err := os.RemoveAll(config.GetStreamsDirPath()); err != nil && !os.IsNotExist(err) {
		logger.Default.Errorf("Stream files deletion failed: %v", err)
	}

	// Remove temporary files
	tmpFiles, err := filepath.Glob(filepath.Join(config.GetConfig().TempPath, "*.m3u"))
	if err == nil {
		for _, file := range tmpFiles {
			if err := os.Remove(file); err != nil {
				logger.Default.Errorf("Temporary file deletion failed: %v", err)
			}
		}
	}
}

// readCacheFromFile reads the current cache content from disk
func readCacheFromFile() string {
	data, err := os.ReadFile(config.GetM3UCachePath())
	if err != nil {
		logger.Default.Errorf("Cache file reading failed: %v", err)
		return "#EXTM3U\n"
	}
	return string(data)
}

// ValidateCache ensures the cache exists and is valid
func ValidateCache() bool {
	if M3uCache.cache.Load() == nil {
		return false
	}

	// Check if cache file exists
	if _, err := os.Stat(config.GetM3UCachePath()); err != nil {
		return false
	}

	// Check if streams directory exists
	if _, err := os.Stat(config.GetStreamsDirPath()); err != nil {
		return false
	}

	return true
}

// GetCacheFilePath returns the path to the current cache file
func GetCacheFilePath() string {
	return config.GetM3UCachePath()
}

// GetStreamsDirPath returns the path to the streams directory
func GetStreamsDirPath() string {
	return config.GetStreamsDirPath()
}

// GetStreamByTitle retrieves a stream from the cache by its title
func GetStreamByTitle(title string) *StreamInfo {
	m3uCache := M3uCache.cache.Load()
	if m3uCache == nil {
		return nil
	}

	shard := m3uCache.processedStreams.getShard(title)
	shard.RLock()
	defer shard.RUnlock()

	if stream, exists := shard.streams[title]; exists {
		return stream.Clone()
	}

	return nil
}

func GetCache() *atomic.Pointer[SortedM3UCache] {
	return &M3uCache.cache
}
