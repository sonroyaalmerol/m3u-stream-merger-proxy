package handlers

import (
	"net/http"
	"os"
	"strings"
	"time"

	"m3u-stream-merger/logger"
)

type M3UHTTPHandler struct {
	logger        logger.Logger
	processedPath string
}

func NewM3UHTTPHandler(logger logger.Logger, processedPath string) *M3UHTTPHandler {
	return &M3UHTTPHandler{
		logger:        logger,
		processedPath: processedPath,
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
	credentials := os.Getenv("CREDENTIALS")
	if credentials == "" || strings.ToLower(credentials) == "none" {
		// No authentication required.
		return true
	}

	creds := h.parseCredentials(credentials)
	user, pass := r.URL.Query().Get("username"), r.URL.Query().Get("password")
	if user == "" || pass == "" {
		return false
	}

	for _, cred := range creds {
		if strings.EqualFold(user, cred[0]) && strings.EqualFold(pass, cred[1]) {
			return true
		}
	}
	return false
}

func (h *M3UHTTPHandler) parseCredentials(raw string) [][]string {
	var result [][]string
	for _, item := range strings.Split(raw, "|") {
		cred := strings.Split(item, ":")
		if len(cred) == 3 {
			if d, err := time.ParseInLocation(time.DateOnly, cred[2], time.Local); err != nil {
				h.logger.Warnf("invalid credential format: %s", item)
				continue
			} else if time.Now().After(d) {
				h.logger.Debugf("Credential expired: %s", item)
				continue
			}
			result = append(result, cred[:2])
		} else {
			result = append(result, cred)
		}
	}
	return result
}
