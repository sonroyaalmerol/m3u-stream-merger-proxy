package m3u

import (
	"fmt"
	"m3u-stream-merger/database"
	"m3u-stream-merger/utils"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
)

type Cache struct {
	sync.Mutex
	data         string
	Revalidating bool
}

var M3uCache = &Cache{}

const cacheFilePath = "/cache.m3u"

func InitCache(db *database.Instance) {
	debug := isDebugMode()

	M3uCache.Lock()
	if M3uCache.Revalidating {
		M3uCache.Unlock()
		if debug {
			utils.SafeLogln("[DEBUG] Cache revalidation is already in progress. Skipping.")
		}
		return
	}
	M3uCache.Revalidating = true
	M3uCache.Unlock()

	content := GenerateAndCacheM3UContent(db, nil)
	err := WriteCacheToFile(content)
	if err != nil {
		utils.SafeLog("Error writing cache to file: %v\n", err)
	}
}

func isDebugMode() bool {
	return os.Getenv("DEBUG") == "true"
}

func getFileExtensionFromUrl(rawUrl string) (string, error) {
	u, err := url.Parse(rawUrl)
	if err != nil {
		return "", err
	}
	return path.Ext(u.Path), nil
}

func GenerateStreamURL(baseUrl string, slug string, sampleUrl string) string {
	ext, err := getFileExtensionFromUrl(sampleUrl)
	if err != nil {
		return fmt.Sprintf("%s/%s\n", baseUrl, slug)
	}
	return fmt.Sprintf("%s/%s%s\n", baseUrl, slug, ext)
}

func GenerateAndCacheM3UContent(db *database.Instance, r *http.Request) string {
	debug := isDebugMode()
	if debug {
		utils.SafeLogln("[DEBUG] Regenerating M3U cache in the background")
	}

	baseUrl := utils.DetermineBaseURL(r)

	if debug {
		utils.SafeLog("[DEBUG] Base URL set to %s\n", baseUrl)
	}

	var content strings.Builder
	content.WriteString("#EXTM3U\n")

	// Retrieve the streams from the database using channels
	streamChan := db.GetStreams()
	for stream := range streamChan {
		if len(stream.URLs) == 0 {
			continue
		}

		if debug {
			utils.SafeLog("[DEBUG] Processing stream with TVG ID: %s\n", stream.TvgID)
		}

		content.WriteString(fmt.Sprintf("#EXTINF:-1 channelID=\"x-ID.%s\" tvg-chno=\"%s\" tvg-id=\"%s\" tvg-name=\"%s\" tvg-logo=\"%s\" group-title=\"%s\",%s\n",
			stream.TvgID, stream.TvgChNo, stream.TvgID, stream.Title, stream.LogoURL, stream.Group, stream.Title))

		content.WriteString(GenerateStreamURL(baseUrl, stream.Slug, stream.URLs[0]))
	}

	if debug {
		utils.SafeLogln("[DEBUG] Finished generating M3U content")
	}

	// Update cache
	M3uCache.Lock()
	M3uCache.data = content.String()
	M3uCache.Revalidating = false
	M3uCache.Unlock()

	return content.String()
}

func ClearCache() {
	debug := isDebugMode()

	M3uCache.Lock()

	if debug {
		utils.SafeLogln("[DEBUG] Clearing memory and disk M3U cache.")
	}
	M3uCache.data = ""
	if err := DeleteCacheFile(); err != nil {
		if debug {
			utils.SafeLog("[DEBUG] Cache file deletion failed: %v\n", err)
		}
	}

	M3uCache.Unlock()
}

func Handler(w http.ResponseWriter, r *http.Request) {
	db, err := database.InitializeDb()
	if err != nil {
		utils.SafeLog("Error initializing Redis database: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	debug := isDebugMode()

	if debug {
		utils.SafeLogln("[DEBUG] Generating M3U content")
	}

	// Set response headers
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	M3uCache.Lock()
	cacheData := M3uCache.data
	M3uCache.Unlock()

	if cacheData == "#EXTM3U\n" || cacheData == "" {
		// Check the file-based cache
		if fileData, err := ReadCacheFromFile(); err == nil {
			cacheData = fileData
			M3uCache.Lock()
			M3uCache.data = fileData // update in-memory cache
			M3uCache.Unlock()
		}
	}

	// serve old cache and regenerate in the background
	if cacheData != "#EXTM3U\n" && cacheData != "" {
		if debug {
			utils.SafeLogln("[DEBUG] Serving old cache and regenerating in background")
		}
		if _, err := w.Write([]byte(cacheData)); err != nil {
			utils.SafeLog("[ERROR] Failed to write response: %v\n", err)
		}

		InitCache(db)

		return
	}

	// If no valid cache, generate content and update cache
	content := GenerateAndCacheM3UContent(db, r)
	go func() {
		if err := WriteCacheToFile(content); err != nil {
			utils.SafeLog("[ERROR] Failed to write cache to file: %v\n", err)
		}
	}()

	if _, err := w.Write([]byte(content)); err != nil {
		utils.SafeLog("[ERROR] Failed to write response: %v\n", err)
	}
}

func ReadCacheFromFile() (string, error) {
	data, err := os.ReadFile(cacheFilePath)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func WriteCacheToFile(content string) error {
	return os.WriteFile(cacheFilePath, []byte(content), 0644)
}

func DeleteCacheFile() error {
	return os.Remove(cacheFilePath)
}
