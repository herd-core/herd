package lifecycle

import (
	"context"
	"time"
)

// StartReaper starts the background loop to clean up workers.
// It should be run in a goroutine.
func (m *Manager) StartReaper(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.sweep()
		}
	}
}

func (m *Manager) sweep() {
	m.mu.RLock()
	// Create a snapshot of IDs to avoid holding the global lock during evaluation
	sessions := make(map[string]*SessionState, len(m.registry))
	for id, state := range m.registry {
		sessions[id] = state
	}
	m.mu.RUnlock()

	now := time.Now()

	for id, state := range sessions {
		state.mu.Lock()
		conns := state.ActiveConns
		created := state.CreatedAt
		lastControl := state.LastControlHeartbeat
		lastData := state.LastDataActivity
		state.mu.Unlock()

		// Rule 1: The Absolute TTL (Squatter Protection)
		if m.Config.AbsoluteTTL > 0 && now.Sub(created) > m.Config.AbsoluteTTL {
			_ = m.UnregisterAndKill(id, "absolute_ttl_expired")
			continue
		}

		// Rule 2: The Dead Client (Network Drop / Deadlock)
		// Only check if HeartbeatGrace is configured
		if m.Config.HeartbeatGrace > 0 && now.Sub(lastControl) > m.Config.HeartbeatGrace {
			_ = m.UnregisterAndKill(id, "client_heartbeat_timeout")
			continue
		}

		// Rule 3: The Idle Abandonment
		if m.Config.IdleTTL > 0 && conns == 0 && now.Sub(lastData) > m.Config.IdleTTL {
			_ = m.UnregisterAndKill(id, "idle_ttl_expired")
			continue
		}
	}
}
