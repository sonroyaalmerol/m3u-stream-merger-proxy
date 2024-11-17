package handlers

import (
	"m3u-stream-merger/store"
	"m3u-stream-merger/utils"
	"net/http"
)

func M3UHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	content := store.RevalidatingGetM3U(r)

	if _, err := w.Write([]byte(content)); err != nil {
		utils.SafeLogf("[ERROR] Failed to write response: %v\n", err)
	}
}
