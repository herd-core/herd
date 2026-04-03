package lifecycle

import (
	"context"
	"sync"
	"time"

	"github.com/herd-core/herd"
)

// WorkerReaper is the interface to your pool to execute a specific worker.
type WorkerReaper interface {
	KillWorker(sessionID string, reason string) error
}

type SessionState struct {
	mu                   sync.Mutex `json:"-"`
	SessionID            string     `json:"session_id"`
	CreatedAt            time.Time  `json:"created_at"`
	LastControlHeartbeat time.Time  `json:"last_control_heartbeat"`
	LastDataActivity     time.Time  `json:"last_data_activity"`
	ActiveConns          int        `json:"active_conns"`

	IdleTTL     time.Duration `json:"idle_ttl"`
	AbsoluteTTL time.Duration `json:"absolute_ttl"`
}

type Manager struct {
	mu       sync.RWMutex
	registry map[string]*SessionState
	reaper   WorkerReaper
}

func NewManager(reaper WorkerReaper) *Manager {
	return &Manager{
		registry: make(map[string]*SessionState),
		reaper:   reaper,
	}
}

// Register explicitly creates the session lease.
func (m *Manager) Register(sessionID string, config herd.TenantConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	// Create a new session entry
	s := &SessionState{
		SessionID:            sessionID,
		CreatedAt:            now,
		LastControlHeartbeat: now,
		LastDataActivity:     now,
		ActiveConns:          0,
		IdleTTL:              time.Duration(config.IdleTimeoutSeconds) * time.Second,
		AbsoluteTTL:          time.Duration(config.TTLSeconds) * time.Second,
	}
	m.registry[sessionID] = s
}

// Remove cleans up the registry after a kill.
func (m *Manager) Remove(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.registry, sessionID)
}

// UnregisterAndKill removes the session from tracking and kills the worker.
// This is used for clean disconnects where we want to ensure no zombie processes.
func (m *Manager) UnregisterAndKill(sessionID string, reason string) error {
	m.mu.Lock()
	delete(m.registry, sessionID)
	m.mu.Unlock()

	// Kill outside the lock to avoid blocking other operations
	return m.reaper.KillWorker(sessionID, reason)
}

// GetSession returns the session state if it exists (for testing or advanced logic)
// Returns nil if not found.
func (m *Manager) GetSession(sessionID string) *SessionState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.registry[sessionID]
}

// ListSessions returns a snapshot of all active sessions.
func (m *Manager) ListSessions() []*SessionState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	sessions := make([]*SessionState, 0, len(m.registry))
	for _, s := range m.registry {
		s.mu.Lock()
		// Return a copy to avoid mutex issues and external mutation
		copy := *s
		s.mu.Unlock()
		sessions = append(sessions, &copy)
	}
	return sessions
}

// StopAll kills all active sessions and removes them from the registry.
func (m *Manager) StopAll(ctx context.Context) error {
	m.mu.Lock()
	sessions := make([]string, 0, len(m.registry))
	for sid := range m.registry {
		sessions = append(sessions, sid)
	}
	m.mu.Unlock()

	for _, sid := range sessions {
		if err := m.UnregisterAndKill(sid, "destroy_all command"); err != nil {
			return err
		}
	}
	return nil
}
