package handlers

import (
	"m3u-stream-merger/store"
	"m3u-stream-merger/utils"
	"net/http"
	"os"
)

func M3UHandler(w http.ResponseWriter, r *http.Request) {
	debug := os.Getenv("DEBUG") == "true"

	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	content := store.RevalidatingGetM3U(r, false)
	_, err := w.Write([]byte(content))
	if err != nil {
		if debug {
			utils.SafeLogf("[DEBUG] Error writing http response: %v\n", err)
		}
	}
}
