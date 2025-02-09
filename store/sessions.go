package store

import (
	"m3u-stream-merger/logger"
	"m3u-stream-merger/utils"
	"net/http"
	"sync"
	"time"
)

type Session struct {
	ID             string
	CreatedAt      time.Time
	TestedIndexes  []string
	InvalidIndexes []string
	Mutex          sync.RWMutex
}

var sessionStore = struct {
	sync.RWMutex
	sessions map[string]*Session
}{sessions: make(map[string]*Session)}

func GetOrCreateSession(r *http.Request) *Session {
	fingerprint := utils.GenerateFingerprint(r)

	sessionStore.RLock()
	session, exists := sessionStore.sessions[fingerprint]
	sessionStore.RUnlock()
	if exists {
		logger.Default.Debugf("Existing session found: %s", fingerprint)
		return session
	}

	session = &Session{
		ID:             fingerprint,
		CreatedAt:      time.Now(),
		TestedIndexes:  []string{},
		InvalidIndexes: []string{},
	}

	sessionStore.Lock()
	sessionStore.sessions[session.ID] = session
	sessionStore.Unlock()

	logger.Default.Debugf("Generating new session: %s", fingerprint)

	return session
}

func ClearSessionStore() {
	sessionStore.Lock()
	for k := range sessionStore.sessions {
		delete(sessionStore.sessions, k)
	}
	sessionStore.Unlock()
}

func (s *Session) SetTestedIndexes(indexes []string) {
	s.TestedIndexes = indexes

	logger.Default.Debugf("Setting tested indexes for session - %s: %v", s.ID, s.TestedIndexes)

	sessionStore.Lock()
	sessionStore.sessions[s.ID] = s
	sessionStore.Unlock()
}

func (s *Session) GetTestedIndexes() []string {
	sessionStore.RLock()
	defer sessionStore.RUnlock()

	return s.TestedIndexes
}

func (s *Session) AddInvalidIndex(index string) {
	s.InvalidIndexes = append(s.InvalidIndexes, index)

	logger.Default.Debugf("Adding invalid index for session - %s: %v", s.ID, s.InvalidIndexes)

	sessionStore.Lock()
	sessionStore.sessions[s.ID] = s
	sessionStore.Unlock()
}

func (s *Session) GetInvalidIndexes() []string {
	sessionStore.RLock()
	defer sessionStore.RUnlock()

	return s.InvalidIndexes
}
