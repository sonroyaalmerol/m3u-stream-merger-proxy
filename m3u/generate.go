package m3u

import (
	"fmt"
	"log"
	"m3u-stream-merger/database"
	"m3u-stream-merger/utils"
	"net/http"
)

func generateStreamURL(baseUrl string, title string) string {
	return fmt.Sprintf("%s/%s.mp4\n", baseUrl, utils.GetStreamUID(title))
}

func GenerateM3UContent(w http.ResponseWriter, r *http.Request) {
	streams, err := database.GetStreams()
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

	for _, stream := range streams {
		// Write #EXTINF line
		_, err := fmt.Fprintf(w, "#EXTINF:-1 tvg-id=\"%s\" tvg-name=\"%s\" tvg-logo=\"%s\" group-title=\"%s\",%s\n",
			stream.TvgID, stream.Title, stream.LogoURL, stream.Group, stream.Title)
		if err != nil {
			continue
		}

		// Write stream URL
		_, err = fmt.Fprintf(w, "%s", generateStreamURL(baseUrl, stream.Title))
		if err != nil {
			continue
		}
	}
}
