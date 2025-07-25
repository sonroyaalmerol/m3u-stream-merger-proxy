package handlers

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"m3u-stream-merger/logger"
)

type Credential struct {
	Username   string    `json:"username"`
	Password   string    `json:"password"`
	Expiration time.Time `json:"expiration"`
}

var credentialsMap = make(map[string]Credential)

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
	w.Header().Set("Access-control-allow-origin", "*")
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
		if strings.EqualFold(user, cred.Username) && strings.EqualFold(pass, cred.Password) {
			return true
		}
	}
	return false
}

func (h *M3UHTTPHandler) parseCredentials(raw string) []Credential {
	var creds []Credential
	err := json.Unmarshal([]byte(raw), &creds)
	if err != nil {
		h.logger.Errorf("error parsing credentials: %v", err)
		return nil
	}
	return creds
}
