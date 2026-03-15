package handlers

import (
	"net/http"
	"sync/atomic"
)

// EPGHTTPHandler serves the merged XMLTV EPG file at /epg.xml.
// It reuses the CREDENTIALS-based auth from the companion M3UHTTPHandler.
type EPGHTTPHandler struct {
	m3uHandler    *M3UHTTPHandler
	processedPath atomic.Pointer[string]
}

func NewEPGHTTPHandler(m3uHandler *M3UHTTPHandler) *EPGHTTPHandler {
	return &EPGHTTPHandler{m3uHandler: m3uHandler}
}

// SetProcessedPath updates the path to the merged EPG file atomically.
func (h *EPGHTTPHandler) SetProcessedPath(path string) {
	h.processedPath.Store(&path)
}

func (h *EPGHTTPHandler) getProcessedPath() string {
	p := h.processedPath.Load()
	if p == nil {
		return ""
	}
	return *p
}

func (h *EPGHTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")

	if !h.m3uHandler.handleAuth(r) {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}

	path := h.getProcessedPath()
	if path == "" {
		http.Error(w, "No EPG data available.", http.StatusNotFound)
		return
	}

	http.ServeFile(w, r, path)
}
