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
	logger.Logf("Creating new M3UHTTPHandler with processedPath: %s", processedPath)
	h := &M3UHTTPHandler{
		logger:        logger,
		processedPath: processedPath,
		credentials:   make(map[string]Credential),
	}
	h.loadCredentials()
	logger.Logf("M3UHTTPHandler created with %d credentials loaded", len(h.credentials))
	return h
}

func (h *M3UHTTPHandler) loadCredentials() {
	credsStr := os.Getenv("CREDENTIALS")
	h.logger.Debugf("Loading credentials from environment variable. Length: %d", len(credsStr))
	
	if credsStr == "" || strings.ToLower(credsStr) == "none" {
		h.logger.Log("No credentials configured, authentication disabled")
		return
	}

	var creds []Credential
	err := json.Unmarshal([]byte(credsStr), &creds)
	if err != nil {
		h.logger.Errorf("error parsing credentials: %v", err)
		return
	}

	h.logger.Logf("Successfully parsed %d credentials from environment", len(creds))
	for i, cred := range creds {
		// Log credential info without sensitive data
		if cred.Expiration.IsZero() {
			h.logger.Debugf("Credential %d: username=%s, password=(redacted), expiration=none",
				i+1, cred.Username)
		} else {
			h.logger.Debugf("Credential %d: username=%s, password=(redacted), expiration=%s",
				i+1, cred.Username, cred.Expiration.Format(time.RFC3339))
		}
		h.credentials[strings.ToLower(cred.Username)] = cred
	}
	
	h.logger.Logf("Loaded %d credentials into memory", len(h.credentials))
}

func (h *M3UHTTPHandler) SetProcessedPath(path string) {
	h.processedPath = path
}

func (h *M3UHTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.logger.Debugf("ServeHTTP called with path: %s", r.URL.Path)
	
	w.Header().Set("Access-control-allow-origin", "*")
	isAuthorized := h.handleAuth(r)
	if !isAuthorized {
		h.logger.Logf("Unauthorized access attempt to %s", r.URL.Path)
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}

	if h.processedPath == "" {
		h.logger.Error("No processed M3U found.")
		http.Error(w, "No processed M3U found.", http.StatusNotFound)
		return
	}

	h.logger.Debugf("Serving file: %s", h.processedPath)
	http.ServeFile(w, r, h.processedPath)
}

func (h *M3UHTTPHandler) handleAuth(r *http.Request) bool {
	h.logger.Debugf("Handling authentication request. Number of loaded credentials: %d", len(h.credentials))
	
	if len(h.credentials) == 0 {
		h.logger.Debug("No credentials loaded, allowing access")
		return true // No authentication required
	}

	user, pass := r.URL.Query().Get("username"), r.URL.Query().Get("password")
	h.logger.Debugf("Authentication attempt with username: %s, password: (redacted)", user)
	
	if user == "" || pass == "" {
		h.logger.Debug("Missing username or password in request")
		return false
	}

	cred, ok := h.credentials[strings.ToLower(user)]
	if !ok {
		h.logger.Debugf("User %s not found in credentials", user)
		return false
	}

	// Constant-time comparison for passwords
	if subtle.ConstantTimeCompare([]byte(pass), []byte(cred.Password)) != 1 {
		h.logger.Debug("Password mismatch for user")
		return false
	}

	// Check expiration
	if !cred.Expiration.IsZero() && time.Now().After(cred.Expiration) {
		h.logger.Debugf("Credential expired for user: %s", user)
		return false
	}

	h.logger.Debug("Authentication successful")
	return true
}
