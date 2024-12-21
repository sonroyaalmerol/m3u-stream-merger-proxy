package store

import (
	"m3u-stream-merger/utils"
	"net/http"
	"os"
	"sync"
	"time"
)

type Session struct {
	ID            string
	CreatedAt     time.Time
	TestedIndexes []int
}

var sessionStore = struct {
	sync.RWMutex
	sessions map[string]Session
}{sessions: make(map[string]Session)}

func GetOrCreateSession(r *http.Request) Session {
	debug := os.Getenv("DEBUG") == "true"
	fingerprint := utils.GenerateFingerprint(r)

	sessionStore.RLock()
	session, exists := sessionStore.sessions[fingerprint]
	sessionStore.RUnlock()
	if exists {
		if debug {
			utils.SafeLogf("[DEBUG] Existing session found: %s\n", fingerprint)
		}
		return session
	}

	session = Session{
		ID:            fingerprint,
		CreatedAt:     time.Now(),
		TestedIndexes: []int{},
	}

	sessionStore.Lock()
	sessionStore.sessions[session.ID] = session
	sessionStore.Unlock()

	if debug {
		utils.SafeLogf("[DEBUG] Generating new session: %s\n", fingerprint)
	}

	return session
}

func ClearSessionStore() {
	sessionStore.Lock()
	for k := range sessionStore.sessions {
		delete(sessionStore.sessions, k)
	}
	sessionStore.Unlock()
}

func (s *Session) SetTestedIndexes(indexes []int) {
	debug := os.Getenv("DEBUG") == "true"

	s.TestedIndexes = indexes

	if debug {
		utils.SafeLogf("[DEBUG] Setting tested indexes for session - %s: %v\n", s.ID, s.TestedIndexes)
	}

	sessionStore.Lock()
	sessionStore.sessions[s.ID] = *s
	sessionStore.Unlock()
}
