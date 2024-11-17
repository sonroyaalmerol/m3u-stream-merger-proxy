package store

import (
	"fmt"
	"m3u-stream-merger/utils"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Cache struct {
	sync.RWMutex
}

var M3uCache = &Cache{}

const cacheFilePath = "/m3u-proxy/data/cache.m3u"

func isDebugMode() bool {
	return os.Getenv("DEBUG") == "true"
}

func RevalidatingGetM3U(r *http.Request, egressStream chan string, force bool) {
	debug := isDebugMode()
	if debug {
		utils.SafeLogln("[DEBUG] Revalidating M3U cache")
	}

	M3uCache.RLock()
	if _, err := os.Stat(cacheFilePath); err != nil || force {
		if debug && !force {
			utils.SafeLogln("[DEBUG] Existing cache not found, generating content")
		}

		M3uCache.RUnlock()

		generateM3UContent(r, egressStream)
		return
	}

	M3uCache.RUnlock()
	readCacheFromFile(egressStream)
}

func generateM3UContent(r *http.Request, egressStream chan string) {
	debug := isDebugMode()
	if debug {
		utils.SafeLogln("[DEBUG] Regenerating M3U cache in the background")
	}

	baseURL := utils.DetermineBaseURL(r)
	if debug {
		utils.SafeLogf("[DEBUG] Base URL set to %s\n", baseURL)
	}

	contentStream := make(chan string)

	if err := writeCacheToFile(egressStream); err != nil {
		utils.SafeLogf("Error writing cache to file: %v\n", err)
	}

	go func() {
		egressStream <- "#EXTM3U\n"

		for {
			data, ok := <-contentStream
			if !ok {
				if debug {
					utils.SafeLogln("[DEBUG] Finished generating M3U content")
				}
				close(egressStream)
				return
			}

			egressStream <- data
		}
	}()

	go func() {
		M3uCache.Lock()
		defer M3uCache.Unlock()

		streams := GetStreams()
		for _, stream := range streams {
			if len(stream.URLs) == 0 {
				continue
			}

			if debug {
				utils.SafeLogf("[DEBUG] Processing stream title: %s\n", stream.Title)
			}

			contentStream <- formatStreamEntry(baseURL, stream)
		}
		close(contentStream)
	}()
}

func ClearCache() {
	debug := isDebugMode()

	M3uCache.Lock()
	defer M3uCache.Unlock()

	if debug {
		utils.SafeLogln("[DEBUG] Clearing memory and disk M3U cache.")
	}
	if err := deleteCacheFile(); err != nil && debug {
		utils.SafeLogf("[DEBUG] Cache file deletion failed: %v\n", err)
	}
}

func readCacheFromFile(dataChan chan string) {
	debug := isDebugMode()

	go func() {
		data, err := os.ReadFile(cacheFilePath)
		if err != nil {
			if debug {
				utils.SafeLogf("[DEBUG] Cache file reading failed: %v\n", err)
			}

			dataChan <- "#EXTM3U\n"
		} else {
			dataChan <- string(data)
		}

		close(dataChan)
	}()
}

func writeCacheToFile(content chan string) error {
	err := os.MkdirAll(filepath.Dir(cacheFilePath), os.ModePerm)
	if err != nil {
		return fmt.Errorf("Error creating directories for data path: %v", err)
	}

	file, err := os.OpenFile(cacheFilePath+".new", os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}

	go func() {
		for {
			data, ok := <-content
			if !ok {
				break
			}
			_, _ = file.WriteString(data)
		}

		_ = os.Remove(cacheFilePath)
		_ = os.Rename(cacheFilePath+".new", cacheFilePath)

		_ = file.Close()
	}()

	return nil
}

func deleteCacheFile() error {
	return os.Remove(cacheFilePath)
}

func formatStreamEntry(baseURL string, stream StreamInfo) string {
	var entry strings.Builder

	extInfTags := []string{"#EXTINF:-1"}
	if stream.TvgID != "" {
		extInfTags = append(extInfTags, fmt.Sprintf("tvg-id=\"%s\"", stream.TvgID))
	}
	if stream.TvgChNo != "" {
		extInfTags = append(extInfTags, fmt.Sprintf("tvg-chno=\"%s\"", stream.TvgChNo))
	}
	if stream.LogoURL != "" {
		extInfTags = append(extInfTags, fmt.Sprintf("tvg-logo=\"%s\"", stream.LogoURL))
	}
	extInfTags = append(extInfTags, fmt.Sprintf("tvg-name=\"%s\"", stream.Title))
	extInfTags = append(extInfTags, fmt.Sprintf("group-title=\"%s\"", stream.Group))

	entry.WriteString(fmt.Sprintf("%s,%s\n", strings.Join(extInfTags, " "), stream.Title))
	entry.WriteString(GenerateStreamURL(baseURL, stream))
	entry.WriteString("\n")

	return entry.String()
}
