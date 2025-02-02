package store

import (
	"fmt"
	"m3u-stream-merger/config"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/utils"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Cache struct {
	sync.RWMutex
	currentStreams []StreamInfo
}

var M3uCache = &Cache{}

func RevalidatingGetM3U(r *http.Request, force bool) string {
	logger.Default.Debug("Revalidating M3U cache")

	if _, err := os.Stat(config.GetM3UCachePath()); err != nil || force {
		if !force {
			logger.Default.Debug("Existing cache not found, generating content")
		}

		return generateM3UContent(r)
	}

	return readCacheFromFile()
}

func generateM3UContent(r *http.Request) string {
	logger.Default.Debug("Regenerating M3U cache in the background")

	baseURL := utils.DetermineBaseURL(r)
	logger.Default.Debugf("Base URL set to %s", baseURL)

	var content strings.Builder

	M3uCache.Lock()
	defer M3uCache.Unlock()

	M3uCache.currentStreams = scanSources()

	content.WriteString("#EXTM3U\n")

	for _, stream := range M3uCache.currentStreams {
		if len(stream.URLs) == 0 {
			continue
		}

		logger.Default.Debugf("Processing stream title: %s", stream.Title)
		content.WriteString(formatStreamEntry(baseURL, stream))
	}

	if err := writeCacheToFile(content.String()); err != nil {
		logger.Default.Errorf("Error writing cache to file: %v", err)
	}

	logger.Default.Log("Background process: Finished building M3U content.")

	return content.String()
}

func GetCurrentStreams() []StreamInfo {
	M3uCache.RLock()
	defer M3uCache.RUnlock()

	return M3uCache.currentStreams
}

func ClearCache() {
	M3uCache.Lock()
	defer M3uCache.Unlock()

	M3uCache.currentStreams = nil

	logger.Default.Debug("Clearing memory and disk M3U cache.")

	if err := os.Remove(config.GetM3UCachePath()); err != nil {
		logger.Default.Debugf("Cache file deletion failed: %v", err)
	}

	if err := os.RemoveAll(config.GetStreamsDirPath()); err != nil {
		logger.Default.Debugf("Stream files deletion failed: %v", err)
	}
}

func readCacheFromFile() string {
	data, err := os.ReadFile(config.GetM3UCachePath())
	if err != nil {
		logger.Default.Debugf("Cache file reading failed: %v", err)

		return "#EXTM3U"
	}

	return string(data)
}

func writeCacheToFile(content string) error {
	err := os.MkdirAll(filepath.Dir(config.GetM3UCachePath()), os.ModePerm)
	if err != nil {
		return err
	}

	err = os.WriteFile(config.GetM3UCachePath()+".new", []byte(content), 0644)
	if err != nil {
		return err
	}

	_ = os.Remove(config.GetM3UCachePath())

	err = os.Rename(config.GetM3UCachePath()+".new", config.GetM3UCachePath())
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
