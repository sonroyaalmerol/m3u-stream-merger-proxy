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
)

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

func GenerateM3UContent(w http.ResponseWriter, r *http.Request, db *database.Instance) {
	debug := os.Getenv("DEBUG") == "true"

	if debug {
		log.Println("DEBUG: Generating M3U content")
	}

	streams, err := db.GetStreams()
	if err != nil {
		log.Println(fmt.Errorf("GetStreams error: %v", err))
	}

	if debug {
		log.Printf("DEBUG: Retrieved %d streams\n", len(streams))
	}

	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	baseUrl := ""
	if r.TLS == nil {
		baseUrl = fmt.Sprintf("http://%s/stream", r.Host)
	} else {
		baseUrl = fmt.Sprintf("https://%s/stream", r.Host)
	}

	if debug {
		log.Printf("DEBUG: Base URL set to %s\n", baseUrl)
	}

	_, err = fmt.Fprintf(w, "#EXTM3U\n")
	if err != nil {
		log.Println(fmt.Errorf("Fprintf error: %v", err))
	}

	for _, stream := range streams {
		if len(stream.URLs) == 0 {
			continue
		}

		if debug {
			log.Printf("DEBUG: Processing stream with TVG ID: %s\n", stream.TvgID)
		}

		_, err := fmt.Fprintf(w, "#EXTINF:-1 channelID=\"x-ID.%s\" tvg-chno=\"%s\" tvg-id=\"%s\" tvg-name=\"%s\" tvg-logo=\"%s\" group-title=\"%s\",%s\n",
			stream.TvgID, stream.TvgChNo, stream.TvgID, stream.Title, stream.LogoURL, stream.Group, stream.Title)
		if err != nil {
			if debug {
				log.Printf("DEBUG: Error writing #EXTINF line for stream %s: %v\n", stream.TvgID, err)
			}
			continue
		}

		_, err = fmt.Fprintf(w, "%s\n", GenerateStreamURL(baseUrl, stream.Slug, stream.URLs[0]))
		if err != nil {
			if debug {
				log.Printf("DEBUG: Error writing stream URL for stream %s: %v\n", stream.TvgID, err)
			}
			continue
		}
	}

	if debug {
		log.Println("DEBUG: Finished generating M3U content")
	}
}
