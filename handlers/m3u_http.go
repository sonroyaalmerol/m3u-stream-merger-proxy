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

type M3UHTTPHandler struct {
	logger        logger.Logger
	processedPath string
	credentials   map[string]Credential
}

func NewM3UHTTPHandler(logger logger.Logger, processedPath string) *M3UHTTPHandler {
	h := &M3UHTTPHandler{
		logger:        logger,
		processedPath: processedPath,
		credentials:   make(map[string]Credential),
	}
	h.loadCredentials()
	return h
}

func (h *M3UHTTPHandler) loadCredentials() {
	credsStr := os.Getenv("CREDENTIALS")
	if credsStr == "" || strings.ToLower(credsStr) == "none" {
		return
	}

	var creds []Credential
	err := json.Unmarshal([]byte(credsStr), &creds)
	if err != nil {
		h.logger.Errorf("error parsing credentials: %v", err)
		return
	}

	for _, cred := range creds {
		h.credentials[strings.ToLower(cred.Username)] = cred
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
	if len(h.credentials) == 0 {
		return true // No authentication required
	}

	user, pass := r.URL.Query().Get("username"), r.URL.Query().Get("password")
	if user == "" || pass == "" {
		return false
	}

	cred, ok := h.credentials[strings.ToLower(user)]
	if !ok {
		return false
	}

	// Constant-time comparison for passwords
	if subtle.ConstantTimeCompare([]byte(pass), []byte(cred.Password)) != 1 {
		return false
	}

	// Check expiration
	if !cred.Expiration.IsZero() && time.Now().After(cred.Expiration) {
		h.logger.Debugf("Credential expired for user: %s", user)
		return false
	}

	return true
}
