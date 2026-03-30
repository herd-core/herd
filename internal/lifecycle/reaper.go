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

	// We don't need to do UnregisterAndKill directly inside the sweep loop because 
	// m.UnregisterAndKill explicitly acquires the global registry lock again (m.mu.Lock()), 
	// which is perfectly safe since we explicitly dropped the lock (RUnlock) before the loop!
	for id, state := range sessions {
		state.mu.Lock()
		conns := state.ActiveConns
		created := state.CreatedAt
		lastData := state.LastDataActivity
		state.mu.Unlock()

		// Rule 1: The Absolute TTL (Squatter Protection)
		if state.AbsoluteTTL > 0 && now.Sub(created) > state.AbsoluteTTL {
			_ = m.UnregisterAndKill(id, "absolute_ttl_expired")
			continue
		}

		// Rule 2: The Dead Client (Network Drop / Deadlock)
		// We can add a heartbeat grace timeout to TenantConfig later if needed.
		// For now we don't have a specific per-tenant config for HeartbeatGrace,
		// so we'll just skip it or leave a placeholder.
		// if m.Config.HeartbeatGrace > 0 && now.Sub(lastControl) > m.Config.HeartbeatGrace {
		// 	_ = m.UnregisterAndKill(id, "client_heartbeat_timeout")
		// 	continue
		// }

		// Rule 3: The Idle Abandonment
		if state.IdleTTL > 0 && conns == 0 && now.Sub(lastData) > state.IdleTTL {
			_ = m.UnregisterAndKill(id, "idle_ttl_expired")
			continue
		}
	}
}
