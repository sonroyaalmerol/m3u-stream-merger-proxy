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
	sync.Mutex
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

	if _, err := os.Stat(cacheFilePath); err != nil || force {
		if debug && !force {
			utils.SafeLogln("[DEBUG] Existing cache not found, generating content")
		}

		return generateM3UContent(r)
	}

	return readCacheFromFile()
}

func generateM3UContent(r *http.Request) string {
	debug := isDebugMode()
	if debug {
		utils.SafeLogln("[DEBUG] Regenerating M3U cache in the background")
	}

	baseURL := utils.DetermineBaseURL(r)
	if debug {
		utils.SafeLogf("[DEBUG] Base URL set to %s\n", baseURL)
	}

	var content strings.Builder

	M3uCache.Lock()
	defer M3uCache.Unlock()

	streams := GetStreams()

	content.WriteString("#EXTM3U\n")

	for _, stream := range streams {
		if len(stream.URLs) == 0 {
			continue
		}

		if debug {
			utils.SafeLogf("[DEBUG] Processing stream title: %s\n", stream.Title)
		}

		content.WriteString(formatStreamEntry(baseURL, stream))
	}

	if err := writeCacheToFile(content.String()); err != nil {
		utils.SafeLogf("[DEBUG] Error writing cache to file: %v\n", err)
	}

	utils.SafeLogln("Background process: Finished building M3U content.")

	return content.String()
}

func ClearCache() {
	debug := isDebugMode()

	M3uCache.Lock()
	defer M3uCache.Unlock()

	if debug {
		utils.SafeLogln("[DEBUG] Clearing memory and disk M3U cache.")
	}
	if err := os.Remove(cacheFilePath); err != nil && debug {
		utils.SafeLogf("[DEBUG] Cache file deletion failed: %v\n", err)
	}
	if err := os.RemoveAll(streamsDirPath); err != nil && debug {
		utils.SafeLogf("[DEBUG] Stream files deletion failed: %v\n", err)
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
		return err
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
	if stream.Group != "" {
		extInfTags = append(extInfTags, fmt.Sprintf("tvg-group=\"%s\"", stream.Group))
		extInfTags = append(extInfTags, fmt.Sprintf("group-title=\"%s\"", stream.Group))
	}
	if stream.TvgType != "" {
		extInfTags = append(extInfTags, fmt.Sprintf("tvg-type=\"%s\"", stream.TvgType))
	}
	if stream.Title != "" {
		extInfTags = append(extInfTags, fmt.Sprintf("tvg-name=\"%s\"", stream.Title))
	}

	entry.WriteString(fmt.Sprintf("%s,%s\n", strings.Join(extInfTags, " "), stream.Title))
	entry.WriteString(GenerateStreamURL(baseURL, stream))
	entry.WriteString("\n")

	return entry.String()
}
