package m3u

import (
	"errors"
	"fmt"
	"log"
	"m3u-stream-merger/database"
	"m3u-stream-merger/utils"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
)

type Cache struct {
	sync.Mutex
	data string
}

var m3uCache = &Cache{}

func getFileExtensionFromUrl(rawUrl string) (string, error) {
	u, err := url.Parse(rawUrl)
	if err != nil {
		return "", err
	}
	pos := strings.LastIndex(u.Path, ".")
	if pos == -1 {
		return "", errors.New("couldn't find a period to indicate a file extension")
	}
	return u.Path[pos+1:], nil
}

func GenerateStreamURL(baseUrl string, slug string, sampleUrl string) string {
	ext, err := getFileExtensionFromUrl(sampleUrl)
	if err != nil {
		return fmt.Sprintf("%s/%s\n", baseUrl, utils.GetStreamUrl(slug))
	}
	return fmt.Sprintf("%s/%s.%s\n", baseUrl, utils.GetStreamUrl(slug), ext)
}

func GenerateAndCacheM3UContent(db *database.Instance, r *http.Request) string {
	debug := os.Getenv("DEBUG") == "true"
	if debug {
		log.Println("[DEBUG] Regenerating M3U cache in the background")
	}

	var content string

	baseUrl := "" // Setup base URL logic
	if r != nil {
		if r.TLS == nil {
			baseUrl = fmt.Sprintf("http://%s/stream", r.Host)
		} else {
			baseUrl = fmt.Sprintf("https://%s/stream", r.Host)
		}
	}

	if customBase, ok := os.LookupEnv("BASE_URL"); ok {
		customBase = strings.TrimSuffix(customBase, "/")
		baseUrl = fmt.Sprintf("%s/stream", customBase)
	}

	if debug {
		utils.SafeLogPrintf(r, nil, "[DEBUG] Base URL set to %s\n", baseUrl)
	}

	content += "#EXTM3U\n"

	// Retrieve the streams from the database using channels
	streamChan := db.GetStreams()
	for stream := range streamChan {
		if len(stream.URLs) == 0 {
			continue
		}

		if debug {
			utils.SafeLogPrintf(nil, nil, "[DEBUG] Processing stream with TVG ID: %s\n", stream.TvgID)
		}

		// Append the stream info to content
		content += fmt.Sprintf("#EXTINF:-1 channelID=\"x-ID.%s\" tvg-chno=\"%s\" tvg-id=\"%s\" tvg-name=\"%s\" tvg-logo=\"%s\" group-title=\"%s\",%s\n",
			stream.TvgID, stream.TvgChNo, stream.TvgID, stream.Title, stream.LogoURL, stream.Group, stream.Title)

		// Append the actual stream URL to content
		content += GenerateStreamURL(baseUrl, stream.Slug, stream.URLs[0])
	}

	if debug {
		log.Println("[DEBUG] Finished generating M3U content")
	}

	// Update cache
	m3uCache.Lock()
	m3uCache.data = content
	m3uCache.Unlock()

	return content
}

func Handler(w http.ResponseWriter, r *http.Request, db *database.Instance) {
	debug := os.Getenv("DEBUG") == "true"

	if debug {
		log.Println("[DEBUG] Generating M3U content")
	}

	// Set response headers
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	m3uCache.Lock()
	cacheData := m3uCache.data
	m3uCache.Unlock()

	if cacheData != "" {
		// Serve cached content if it's still valid
		if debug {
			log.Println("[DEBUG] Serving M3U content from cache")
		}
		_, _ = w.Write([]byte(cacheData))
		return
	}

	// If cache is expired, serve old cache and regenerate in the background
	if cacheData != "" {
		if debug {
			log.Println("[DEBUG] Cache expired, serving old cache and regenerating in background")
		}
		_, _ = w.Write([]byte(cacheData))
		go GenerateAndCacheM3UContent(db, r)
		return
	}

	// If no valid cache, generate content and update cache
	content := GenerateAndCacheM3UContent(db, r)
	_, _ = w.Write([]byte(content))
}
