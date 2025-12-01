package executor

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// DroidMessage represents a single message in conversation
type DroidMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// DroidSession tracks a conversation session with a droid worker
type DroidSession struct {
	ID           string
	SystemPrompt string
	Messages     []DroidMessage
	Worker       *DroidWorker
	LastAccess   time.Time
	mu           sync.Mutex
}

// DroidSessionManager manages sessions by system prompt hash
type DroidSessionManager struct {
	sessions   map[string]*DroidSession
	sessionTTL time.Duration
	mu         sync.RWMutex
	closed     bool
}

// NewDroidSessionManager creates a new session manager
func NewDroidSessionManager(ttl time.Duration) *DroidSessionManager {
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	sm := &DroidSessionManager{
		sessions:   make(map[string]*DroidSession),
		sessionTTL: ttl,
	}
	go sm.cleanupLoop()
	return sm
}

// computeSessionKey generates a unique key from system prompt and model
func computeSessionKey(systemPrompt, model string) string {
	h := sha256.New()
	h.Write([]byte(systemPrompt))
	h.Write([]byte("|"))
	h.Write([]byte(model))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// GetOrCreateSession returns existing session or creates new one
func (sm *DroidSessionManager) GetOrCreateSession(systemPrompt, model string) (*DroidSession, bool) {
	key := computeSessionKey(systemPrompt, model)

	sm.mu.RLock()
	session, exists := sm.sessions[key]
	sm.mu.RUnlock()

	if exists {
		session.mu.Lock()
		session.LastAccess = time.Now()
		session.mu.Unlock()
		return session, true
	}

	// Create new session
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Double-check after acquiring write lock
	if session, exists = sm.sessions[key]; exists {
		session.mu.Lock()
		session.LastAccess = time.Now()
		session.mu.Unlock()
		return session, true
	}

	session = &DroidSession{
		ID:           key,
		SystemPrompt: systemPrompt,
		Messages:     make([]DroidMessage, 0),
		LastAccess:   time.Now(),
	}
	sm.sessions[key] = session
	log.Debugf("droid session: created new session %s", key)
	return session, false
}

// GetSession returns existing session or nil
func (sm *DroidSessionManager) GetSession(systemPrompt, model string) *DroidSession {
	key := computeSessionKey(systemPrompt, model)

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if session, exists := sm.sessions[key]; exists {
		session.mu.Lock()
		session.LastAccess = time.Now()
		session.mu.Unlock()
		return session
	}
	return nil
}

// RemoveSession removes a session
func (sm *DroidSessionManager) RemoveSession(systemPrompt, model string) {
	key := computeSessionKey(systemPrompt, model)

	sm.mu.Lock()
	defer sm.mu.Unlock()

	if session, exists := sm.sessions[key]; exists {
		if session.Worker != nil {
			session.Worker.Close()
		}
		delete(sm.sessions, key)
		log.Debugf("droid session: removed session %s", key)
	}
}

// AddMessages adds messages to session history
func (s *DroidSession) AddMessages(messages []DroidMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Messages = append(s.Messages, messages...)
	s.LastAccess = time.Now()
}

// SetMessages replaces session history
func (s *DroidSession) SetMessages(messages []DroidMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Messages = make([]DroidMessage, len(messages))
	copy(s.Messages, messages)
	s.LastAccess = time.Now()
}

// GetMessages returns copy of session history
func (s *DroidSession) GetMessages() []DroidMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]DroidMessage, len(s.Messages))
	copy(result, s.Messages)
	return result
}

// MuLock locks the session mutex (for debugging/testing)
func (s *DroidSession) MuLock() {
	s.mu.Lock()
}

// MuUnlock unlocks the session mutex (for debugging/testing)
func (s *DroidSession) MuUnlock() {
	s.mu.Unlock()
}

func (s *DroidSession) FindNewMessages(incoming []DroidMessage) []DroidMessage {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.Messages) == 0 {
		return incoming
	}

	// Find where the histories diverge
	historyLen := len(s.Messages)
	incomingLen := len(incoming)

	// Messages should be a superset of history
	if incomingLen < historyLen {
		// Incoming is shorter - treat as new conversation
		return incoming
	}

	// Check if incoming starts with our history
	for i := 0; i < historyLen; i++ {
		if i >= incomingLen ||
			s.Messages[i].Role != incoming[i].Role ||
			s.Messages[i].Content != incoming[i].Content {
			// Histories don't match - treat as new conversation
			return incoming
		}
	}

	// Return only new messages
	if incomingLen > historyLen {
		return incoming[historyLen:]
	}

	// No new messages
	return nil
}

// cleanupLoop periodically removes expired sessions
func (sm *DroidSessionManager) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		sm.mu.Lock()
		if sm.closed {
			sm.mu.Unlock()
			return
		}

		now := time.Now()
		for key, session := range sm.sessions {
			session.mu.Lock()
			if now.Sub(session.LastAccess) > sm.sessionTTL {
				if session.Worker != nil {
					session.Worker.Close()
				}
				delete(sm.sessions, key)
				log.Debugf("droid session: expired session %s", key)
			}
			session.mu.Unlock()
		}
		sm.mu.Unlock()
	}
}

// Close shuts down the session manager
func (sm *DroidSessionManager) Close() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.closed = true
	for _, session := range sm.sessions {
		if session.Worker != nil {
			session.Worker.Close()
		}
	}
	sm.sessions = make(map[string]*DroidSession)
	log.Debug("droid session: manager closed")
}

// Stats returns session statistics
func (sm *DroidSessionManager) Stats() (total int, active int) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	total = len(sm.sessions)
	now := time.Now()
	for _, session := range sm.sessions {
		session.mu.Lock()
		if now.Sub(session.LastAccess) < 5*time.Minute {
			active++
		}
		session.mu.Unlock()
	}
	return
}
