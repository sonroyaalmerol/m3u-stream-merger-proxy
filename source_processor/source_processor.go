package sourceproc

import (
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"m3u-stream-merger/config"
	"m3u-stream-merger/logger"
)

type M3UCache struct {
	cache           atomic.Pointer[M3UManager]
	slugToInfoCache sync.Map // map[string]*StreamInfo
}

var M3uCache = &M3UCache{}

func RevalidatingGetM3U(r *http.Request, force bool) string {
	// Grab the current cache if it exists.
	currentCache := M3uCache.cache.Load()

	// If there is an existing cache, check if it’s still being processed.
	if currentCache != nil {
		if currentCache.IsProcessing() {
			// Wait until initial revalidation is complete.
			<-currentCache.initialDone
		}
		return readCacheFromFile()
	}

	// No cache exists yet ➜ create a new one.
	newCache := NewM3UManager(r)
	if newCache == nil {
		return "#EXTM3U\n"
	}

	if force {
		// For forced revalidation remove the previous file.
		if err := os.Remove(config.GetM3UCachePath()); err != nil &&
			!os.IsNotExist(err) {
			logger.Default.Errorf("Error removing existing cache: %v", err)
		}
		M3uCache.cache.Store(newCache)
	} else {
		// If another goroutine raced and created one, use that.
		if !M3uCache.cache.CompareAndSwap(nil, newCache) {
			currentCache = M3uCache.cache.Load()
			if currentCache != nil && currentCache.IsProcessing() {
				<-currentCache.initialDone
			}
			return readCacheFromFile()
		}
	}

	logger.Default.Log("Starting initial M3U cache processing...")

	// Trigger processing synchronously here.
	updates := newCache.processM3UsInRealTime()
	// Block until the update channel is closed (i.e. revalidation is complete).
	processCount := 0
	for range updates {
		processCount++
		batch := int(math.Pow(10, math.Floor(math.Log10(float64(processCount)))))
		if batch < 100 {
			batch = 100
		}
		if processCount%batch == 0 {
			logger.Default.Logf("Processed %d streams so far", processCount)
		}
	}
	logger.Default.Logf("Completed processing %d total streams", processCount)

	// At this point the initial revalidation is finished.
	return readCacheFromFile()
}

// GetCurrentStreams returns a map of all currently processed streams
func GetCurrentStreams() map[string]*StreamInfo {
	m3uCache := M3uCache.cache.Load()
	if m3uCache != nil {
		return m3uCache.processedStreams.toMap()
	}
	return make(map[string]*StreamInfo)
}

// ClearCache clears both memory and disk cache
func ClearCache() {
	m3uCache := M3uCache.cache.Load()
	if m3uCache != nil {
		m3uCache.processedStreams.clear()
		M3uCache.cache.Store(nil)
	}

	M3uCache.slugToInfoCache.Clear()

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

func GetCache() *atomic.Pointer[M3UManager] {
	return &M3uCache.cache
}
