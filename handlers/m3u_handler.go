package handlers

import (
	"net/http"
	"os"
	"strings"
	"time"

	"m3u-stream-merger/logger"
	"m3u-stream-merger/store"
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
	w.Header().Set("Access-Control-Allow-Origin", "*")
	content := h.handleAuth(r)
	if content == "" {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	content = store.RevalidatingGetM3U(r, false)
	if _, err := w.Write([]byte(content)); err != nil {
		h.logger.Debugf("Error writing http response: %v", err)
	}
}

func (h *M3UHandler) handleAuth(r *http.Request) string {
	credentials := os.Getenv("CREDENTIALS")
	if credentials == "" || strings.ToLower(credentials) == "none" {
		// No authentication required.
		return "ok"
	}

	creds := parseCredentials(credentials, h.logger)
	user, pass := r.URL.Query().Get("username"), r.URL.Query().Get("password")
	if user == "" || pass == "" {
		return ""
	}

	for _, cred := range creds {
		if strings.EqualFold(user, cred[0]) && strings.EqualFold(pass, cred[1]) {
			return "ok"
		}
	}
	return ""
}

func parseCredentials(raw string, log logger.Logger) [][]string {
	var result [][]string
	for _, item := range strings.Split(raw, "|") {
		cred := strings.Split(item, ":")
		if len(cred) == 3 {
			if d, err := time.Parse(time.DateOnly, cred[2]); err != nil {
				log.Warnf("invalid credential format: %s", item)
				continue
			} else if time.Now().After(d) {
				log.Debugf("Credential expired: %s", item)
				continue
			}
			result = append(result, cred[:2])
		} else {
			result = append(result, cred)
		}
	}
	return result
}
