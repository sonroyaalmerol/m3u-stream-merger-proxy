package handlers

import (
	"net/http"
	"os"
	"strings"
	"time"

	"m3u-stream-merger/logger"
	"m3u-stream-merger/store"
)

type M3UHTTPHandler struct {
	logger logger.Logger
	Cache  *store.Cache
}

func NewM3UHTTPHandler(logger logger.Logger) *M3UHTTPHandler {
	return &M3UHTTPHandler{
		logger: logger,
		Cache:  store.M3uCache,
	}
}

func (h *M3UHTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	isAuthorized := h.handleAuth(r)
	if !isAuthorized {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	content := store.RevalidatingGetM3U(r, false)
	if _, err := w.Write([]byte(content)); err != nil {
		h.logger.Debugf("Error writing http response: %v", err)
	}
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
			if d, err := time.Parse(time.DateOnly, cred[2]); err != nil {
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
