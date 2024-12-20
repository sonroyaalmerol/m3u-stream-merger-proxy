package store

import (
	"m3u-stream-merger/utils"
	"net/http"
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
	fingerprint := utils.GenerateFingerprint(r)

	sessionStore.RLock()
	session, exists := sessionStore.sessions[fingerprint]
	sessionStore.RUnlock()
	if exists {
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

	return session
}

func (s *Session) SetTestedIndexes(indexes []int) {
	s.TestedIndexes = indexes

	sessionStore.Lock()
	sessionStore.sessions[s.ID] = *s
	sessionStore.Unlock()
}
