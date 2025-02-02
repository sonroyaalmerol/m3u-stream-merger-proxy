package handlers

import (
	"m3u-stream-merger/logger"
	"m3u-stream-merger/store"
	"net/http"
)

type M3UHandler struct {
	logger logger.Logger
	Cache  *store.Cache
}

func NewM3UHandler(logger logger.Logger) *M3UHandler {
	return &M3UHandler{
		logger: logger,
		Cache:  store.M3uCache,
	}
}

func (h *M3UHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	content := store.RevalidatingGetM3U(r, false)

	if _, err := w.Write([]byte(content)); err != nil {
	h.logger.Debugf("Error writing http response: %v\n", err)
	}
}
