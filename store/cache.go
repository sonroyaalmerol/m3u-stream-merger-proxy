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

func RevalidatingGetM3U(r *http.Request, force bool) string {
	debug := isDebugMode()
	if debug {
		utils.SafeLogln("[DEBUG] Revalidating M3U cache")
	}

	if force {
		return generateM3UContent(r)
	}

	if _, err := os.Stat(cacheFilePath); err == nil {
		if debug {
			utils.SafeLogln("[DEBUG] M3U cache already exists")
		}

		return readCacheFromFile()
	}

	if debug {
		utils.SafeLogln("[DEBUG] Existing cache not found, generating content")
	}

	if !M3uCache.TryRLock() {
		return readCacheFromFile()
	}

	return generateM3UContent(r)
}

func generateM3UContent(r *http.Request) string {
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

	var content strings.Builder
	content.WriteString("#EXTM3U\n")

	streams := GetStreams()
	for _, stream := range streams {
		if len(stream.URLs) == 0 {
			continue
		}

		if debug {
			utils.SafeLogf("[DEBUG] Processing stream with TVG ID: %s\n", stream.TvgID)
		}

		content.WriteString(formatStreamEntry(baseURL, stream))
	}

	if debug {
		utils.SafeLogln("[DEBUG] Finished generating M3U content")
	}

	stringContent := content.String()

	if err := writeCacheToFile(stringContent); err != nil {
		utils.SafeLogf("Error writing cache to file: %v\n", err)
	}

	return stringContent
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

func readCacheFromFile() string {
	debug := isDebugMode()

	data, err := os.ReadFile(cacheFilePath)
	if err != nil {
		if debug {
			utils.SafeLogf("[DEBUG] Cache file reading failed: %v\n", err)
		}

		return "#EXTM3U\n"
	}
	return string(data)
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
