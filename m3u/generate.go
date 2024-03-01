package m3u

import (
	"errors"
	"fmt"
	"log"
	"m3u-stream-merger/utils"
	"m3u-stream-merger/database"
	"net/http"
)

func GenerateM3UContent(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain") // Set the Content-Type header to M3U
	w.Header().Set("Access-Control-Allow-Origin", "*")

	baseUrl := ""
	if r.TLS == nil {
		baseUrl = fmt.Sprintf("http://%s/stream", r.Host)
	} else {
		baseUrl = fmt.Sprintf("https://%s/stream", r.Host)
	}

	_, err := fmt.Fprintf(w, "#EXTM3U\n")
	if err != nil {
		log.Println(fmt.Errorf("Fprintf error: %v", err))
	}

	for _, stream := range Streams {
		// Write #EXTINF line
		_, err := fmt.Fprintf(w, "#EXTINF:-1 tvg-id=\"%s\" tvg-name=\"%s\" tvg-logo=\"%s\" group-title=\"%s\",%s\n",
			stream.TvgID, stream.Title, stream.LogoURL, stream.Group, stream.Title)
		if err != nil {
			continue
		}

		// Write stream URL
		_, err = fmt.Fprintf(w, "%s/%s.mp4\n", baseUrl, utils.GetStreamUID(stream.Title))
		if err != nil {
			continue
		}
	}
}

func FindStreamByName(streamName string) (*database.StreamInfo, error) {
	for _, s := range Streams {
		if s.Title == streamName {
			return &s, nil
		}
	}

	return &database.StreamInfo{}, errors.New("stream not found")
}
