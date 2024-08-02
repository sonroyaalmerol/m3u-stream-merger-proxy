package m3u

import (
	"errors"
	"fmt"
	"log"
	"m3u-stream-merger/database"
	"m3u-stream-merger/utils"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// getFileExtensionFromUrl
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

// GenerateStreamURL
func GenerateStreamURL(baseUrl string, title string, sampleUrl string) string {
	ext, err := getFileExtensionFromUrl(sampleUrl)
	if err != nil {
		return fmt.Sprintf("%s/%s\n", baseUrl, utils.GetStreamUID(title))
	}
	return fmt.Sprintf("%s/%s.%s\n", baseUrl, utils.GetStreamUID(title), ext)
}

// GenerateM3UContent
func GenerateM3UContent(w http.ResponseWriter, r *http.Request, db *database.Instance) {
	streams, err := db.GetStreams()
	if err != nil {
		log.Println(fmt.Errorf("GetStreams error: %v", err))
	}

	w.Header().Set("Content-Type", "text/plain") // Set the Content-Type header to M3U
	w.Header().Set("Access-Control-Allow-Origin", "*")

	baseUrl := ""
	if r.TLS == nil {
		baseUrl = fmt.Sprintf("http://%s/stream", r.Host)
	} else {
		baseUrl = fmt.Sprintf("https://%s/stream", r.Host)
	}

	_, err = fmt.Fprintf(w, "#EXTM3U\n")
	if err != nil {
		log.Println(fmt.Errorf("Fprintf error: %v", err))
	}

	// Sort the streams by TVG ID, interpret the IDs as numbers
	sort.Slice(streams, func(i, j int) bool {
		id1, err1 := strconv.Atoi(streams[i].TvgID)
		id2, err2 := strconv.Atoi(streams[j].TvgID)
		if err1 != nil || err2 != nil {
			// If any of the conversions fail, compare as strings
			return streams[i].TvgID < streams[j].TvgID
		}
		return id1 < id2
	})

	for _, stream := range streams {
		if len(stream.URLs) == 0 {
			continue
		}

		// Write #EXTINF line
		_, err := fmt.Fprintf(w, "#EXTINF:-1 channelID=\"x-ID.%s\" tvg-chno=\"%s\" tvg-id=\"%s\" tvg-name=\"%s\" tvg-logo=\"%s\" group-title=\"%s\",%s\n",
			stream.TvgID, stream.TvgID, stream.TvgID, stream.Title, stream.LogoURL, stream.Group, stream.Title)
		if err != nil {
			continue
		}

		// Write stream URL
		_, err = fmt.Fprintf(w, "%s", GenerateStreamURL(baseUrl, stream.Title, stream.URLs[0].Content))
		if err != nil {
			continue
		}
	}
}
