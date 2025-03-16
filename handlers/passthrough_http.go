package handlers

import (
	"encoding/base64"
	"io"
	"net/http"
	"strings"

	"m3u-stream-merger/logger"
	"m3u-stream-merger/utils"
)

type PassthroughHTTPHandler struct {
	logger logger.Logger
}

func NewPassthroughHTTPHandler(logger logger.Logger) *PassthroughHTTPHandler {
	return &PassthroughHTTPHandler{
		logger: logger,
	}
}

func (h *PassthroughHTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	const prefix = "/a/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		h.logger.Error("Invalid URL path: missing " + prefix)
		http.Error(w, "Invalid URL provided", http.StatusBadRequest)
		return
	}

	encodedURL := r.URL.Path[len(prefix):]
	if encodedURL == "" {
		h.logger.Error("No encoded URL provided in the path")
		http.Error(w, "No URL provided", http.StatusBadRequest)
		return
	}

	originalURLBytes, err := base64.URLEncoding.DecodeString(encodedURL)
	if err != nil {
		h.logger.Error("Failed to decode original URL: " + err.Error())
		http.Error(w, "Failed to decode original URL", http.StatusBadRequest)
		return
	}

	originalURL := string(originalURLBytes)

	proxyReq, err := http.NewRequest(r.Method, originalURL, r.Body)
	if err != nil {
		h.logger.Error("Failed to create new request: " + err.Error())
		http.Error(w, "Error creating request", http.StatusInternalServerError)
		return
	}

	proxyReq = proxyReq.WithContext(r.Context())
	proxyReq.Header = r.Header.Clone()

	resp, err := utils.HTTPClient.Do(proxyReq)
	if err != nil {
		h.logger.Error("Failed to fetch original URL: " + err.Error())
		http.Error(w, "Error fetching the requested resource", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	w.WriteHeader(resp.StatusCode)

	if _, err := io.Copy(w, resp.Body); err != nil {
		h.logger.Error("Failed to write response body: " + err.Error())
	}
}
