package lifecycle

import (
	"sync"
	"time"

	"github.com/herd-core/herd"
)

// WorkerReaper is the interface to your pool to execute a specific worker.
type WorkerReaper interface {
	KillWorker(sessionID string, reason string) error
}

type SessionState struct {
	mu                   sync.Mutex
	SessionID            string
	CreatedAt            time.Time
	LastControlHeartbeat time.Time
	LastDataActivity     time.Time
	ActiveConns          int

	IdleTTL        time.Duration
	AbsoluteTTL    time.Duration
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
		// Force IdleTTL to 0 (infinity) for now as requested.
		IdleTTL:              0, // time.Duration(config.IdleTimeoutSeconds) * time.Second,
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
