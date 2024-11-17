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

func RevalidatingGetM3U(r *http.Request, force bool) chan string {
	debug := isDebugMode()
	if debug {
		utils.SafeLogln("[DEBUG] Revalidating M3U cache")
	}

	if _, err := os.Stat(cacheFilePath); err != nil || force {
		if debug && !force {
			utils.SafeLogln("[DEBUG] Existing cache not found, generating content")
		}

		return generateM3UContent(r)
	}

	return readCacheFromFile()
}

func generateM3UContent(r *http.Request) chan string {
	M3uCache.Lock()
	defer M3uCache.Unlock()

	debug := isDebugMode()
	if debug {
		utils.SafeLogln("[DEBUG] Regenerating M3U cache in the background")
	}

	baseURL := utils.DetermineBaseURL(r)
	if debug {
		utils.SafeLogf("[DEBUG] Base URL set to %s\n", baseURL)
	}

	contentStream := make(chan string)
	egressStream := make(chan string)

	go func() {
		var content strings.Builder
		content.WriteString("#EXTM3U\n")
		egressStream <- "#EXTM3U\n"

		for {
			data, ok := <-contentStream
			if !ok {
				if debug {
					utils.SafeLogln("[DEBUG] Finished generating M3U content")
				}
				close(egressStream)
				break
			}

			egressStream <- data
			content.WriteString(data)
		}

		stringContent := content.String()

		if err := writeCacheToFile(stringContent); err != nil {
			utils.SafeLogf("Error writing cache to file: %v\n", err)
		}
	}()

	go func() {
		streams := GetStreams()
		for _, stream := range streams {
			if len(stream.URLs) == 0 {
				continue
			}

			if debug {
				utils.SafeLogf("[DEBUG] Processing stream with TVG ID: %s\n", stream.TvgID)
			}

			contentStream <- formatStreamEntry(baseURL, stream)
		}
		close(contentStream)
	}()

	return egressStream
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

func readCacheFromFile() chan string {
	debug := isDebugMode()

	dataChan := make(chan string)
	go func() {
		data, err := os.ReadFile(cacheFilePath)
		if err != nil {
			if debug {
				utils.SafeLogf("[DEBUG] Cache file reading failed: %v\n", err)
			}

			dataChan <- "#EXTM3U\n"
		}
		dataChan <- string(data)
	}()

	return dataChan
}

func writeCacheToFile(content string) error {
	err := os.MkdirAll(filepath.Dir(cacheFilePath), os.ModePerm)
	if err != nil {
		return fmt.Errorf("Error creating directories for data path: %v", err)
	}

	err = os.WriteFile(cacheFilePath+".new", []byte(content), 0644)
	if err != nil {
		return err
	}

	_ = os.Remove(cacheFilePath)
	err = os.Rename(cacheFilePath+".new", cacheFilePath)
	if err != nil {
		return err
	}

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
