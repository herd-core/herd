package lifecycle

import (
	"context"
	"time"
)

// UpdateHeartbeat is called by the gRPC stream when a Ping arrives.
func (m *Manager) UpdateHeartbeat(sessionID string) {
	m.mu.RLock()
	state, exists := m.registry[sessionID]
	m.mu.RUnlock()
	if !exists {
		return
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	state.LastControlHeartbeat = time.Now()
}

// BeginRequest is called by the proxy before routing.
// This is used for both REST and WebSockets.
func (m *Manager) BeginRequest(sessionID string) {
	m.mu.RLock()
	state, exists := m.registry[sessionID]
	m.mu.RUnlock()
	if !exists {
		return
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	state.ActiveConns++
}

// EndRequest is deferred by the proxy. It catches deadlocks.
func (m *Manager) EndRequest(sessionID string, proxyErr error) {
	m.mu.RLock()
	state, exists := m.registry[sessionID]
	m.mu.RUnlock()
	if !exists {
		return
	}

	state.mu.Lock()
	state.ActiveConns--
	if state.ActiveConns < 0 {
		state.ActiveConns = 0 // Safety net
	}
	state.LastDataActivity = time.Now()
	state.mu.Unlock()

	// DEADLOCK GUILLOTINE: If the context timed out, the worker is stuck. Kill it now.
	if proxyErr == context.DeadlineExceeded {
		// We can't rely on reaper to pick this up quickly enough, kill it now.
		// KillWorker is assumed to be idempotent.
		_ = m.reaper.KillWorker(sessionID, "data_plane_deadlock")
		// Clean up registry to prevent further attempts/confusion
		m.Remove(sessionID)
	}
}
