package handlers

import (
	"net/http"

	"m3u-stream-merger/logger"
)

type M3UHTTPHandler struct {
	logger        logger.Logger
	processedPath string
	auth          *CredentialsAuth
}

func NewM3UHTTPHandler(logger logger.Logger, processedPath string) *M3UHTTPHandler {
	return &M3UHTTPHandler{
		logger:        logger,
		processedPath: processedPath,
		auth:          NewCredentialsAuth(logger),
	}
}

func (h *M3UHTTPHandler) SetProcessedPath(path string) {
	h.processedPath = path
}

func (h *M3UHTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	isAuthorized := h.handleAuth(r)
	if !isAuthorized {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}

	if h.processedPath == "" {
		http.Error(w, "No processed M3U found.", http.StatusNotFound)
		return
	}

	http.ServeFile(w, r, h.processedPath)
}

func (h *M3UHTTPHandler) handleAuth(r *http.Request) bool {
	return h.auth.Authorize(r.URL.Query().Get("username"), r.URL.Query().Get("password"))
}
