package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/herd-core/herd"
	"github.com/herd-core/herd/internal/lifecycle"
	"github.com/herd-core/herd/internal/vsock"
)

type ControlPlaneHandler struct {
	pool             *herd.Pool[*http.Client]
	lifecycleManager *lifecycle.Manager
	logger           *EventLogger
	proxyAddress     string
	seq              atomic.Uint64
}

func NewControlPlaneHandler(
	pool *herd.Pool[*http.Client],
	lifecycleManager *lifecycle.Manager,
	proxyAddress string,
	logger *EventLogger,
) http.Handler {
	h := &ControlPlaneHandler{
		pool:             pool,
		lifecycleManager: lifecycleManager,
		proxyAddress:     proxyAddress,
		logger:           logger,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions", h.handleCreateSession)
	mux.HandleFunc("DELETE /v1/sessions/", h.handleDeleteSession) // /v1/sessions/{id}
	mux.HandleFunc("GET /v1/sessions/", h.handleLogsSession)      // /v1/sessions/{id}/logs
	mux.HandleFunc("POST /v1/sessions/", h.handleExecSession)     // /v1/sessions/{id}/exec
	mux.HandleFunc("PUT /v1/sessions/", h.handleHeartbeat)        // /v1/sessions/{id}/heartbeat

	return mux
}

func (h *ControlPlaneHandler) handleLogsSession(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 4 || parts[3] != "logs" {
		http.Error(w, "invalid path", http.StatusNotFound)
		return
	}
	sessionID := parts[2]

	sess, err := h.pool.GetSession(r.Context(), sessionID)
	if err != nil || sess == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	// Assuming log file is stored magically in /tmp/id.log because of firecracker factory
	// Wait, the id from pool is not the firecracker worker id (f.id). The Firecracker worker id is randomly generated in Spawn.
	// Oh! I should have captured log to /tmp/{sessionID}.log!
	// Or we can just grab f.id!
	var workerID string
	if fw, ok := sess.Worker.(interface{ ID() string }); ok {
		workerID = fw.ID()
	} else {
		http.Error(w, "worker does not support logs", http.StatusBadRequest)
		return
	}

	logPath := fmt.Sprintf("/tmp/%s.log", workerID)
	
	w.Header().Set("Content-Type", "text/plain")
	f, err := os.Open(logPath)
	if err != nil {
		http.Error(w, "logs not available: " + err.Error(), 404)
		return
	}
	defer f.Close()

	// In a real app we'd tail this, but io.Copy is fine for now
	io.Copy(w, f)
}

func (h *ControlPlaneHandler) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 4 || parts[3] != "heartbeat" {
		http.Error(w, "invalid path", http.StatusNotFound)
		return
	}
	sessionID := parts[2]
	h.lifecycleManager.UpdateHeartbeat(sessionID)
	w.WriteHeader(http.StatusOK)
}

// SessionCreateRequest is the JSON body for POST /v1/sessions
type SessionCreateRequest struct {
	Image              string            `json:"image"`
	Env                map[string]string `json:"env"`
	IdleTimeoutSeconds int               `json:"idle_timeout_seconds"`
}

// SessionCreateResponse is the JSON response
type SessionCreateResponse struct {
	SessionID    string `json:"session_id"`
	InternalIP   string `json:"internal_ip"`
	ProxyAddress string `json:"proxy_address"`
}

func (h *ControlPlaneHandler) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	RecordAcquireRequest()

	var req SessionCreateRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	sessionID := fmt.Sprintf("sess-%d-%d", time.Now().UnixNano(), h.seq.Add(1))
	h.logger.Info("acquire_request_received", map[string]any{"session_id": sessionID})

	session, err := h.pool.Acquire(r.Context(), sessionID)
	if err != nil {
		RecordAcquireFailure()
		h.logger.Error("session_acquire_failed", map[string]any{"session_id": sessionID, "error": err})
		http.Error(w, fmt.Sprintf("failed to acquire session: %v", err), http.StatusInternalServerError)
		return
	}

	h.lifecycleManager.Register(sessionID)
	RecordSessionStarted()
	h.logger.Info("session_acquired", map[string]any{"session_id": sessionID})

	var internalIP string
	// Try to get GuestIP if it's a Firecracker worker
	if fw, ok := session.Worker.(interface{ GuestIP() string }); ok {
		internalIP = fw.GuestIP()
	}

	resp := SessionCreateResponse{
		SessionID:    sessionID,
		InternalIP:   internalIP,
		ProxyAddress: h.proxyAddress,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *ControlPlaneHandler) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	// Extract sessionID from URL: /v1/sessions/{id}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 3 {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	sessionID := parts[2]

	err := h.lifecycleManager.UnregisterAndKill(sessionID, "api_requested")
	if err != nil {
		h.logger.Error("session_cleanup_failed", map[string]any{"session_id": sessionID, "error": err})
		http.Error(w, fmt.Sprintf("failed to kill session: %v", err), http.StatusInternalServerError)
		return
	}

	RecordSessionKilled()
	h.logger.Info("session_killed", map[string]any{"session_id": sessionID})
	w.WriteHeader(http.StatusOK)
}

func (h *ControlPlaneHandler) handleExecSession(w http.ResponseWriter, r *http.Request) {
	// Path: /v1/sessions/{id}/exec
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 4 || parts[3] != "exec" {
		http.Error(w, "invalid path", http.StatusNotFound)
		return
	}
	sessionID := parts[2]

	session, err := h.pool.GetSession(r.Context(), sessionID)
	if err != nil || session == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	// Address() returns unix://<path>
	addr := session.Worker.Address()
	if !strings.HasPrefix(addr, "unix://") {
		http.Error(w, "worker does not support local vsock exec", http.StatusBadRequest)
		return
	}
	socketPath := strings.TrimPrefix(addr, "unix://")

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "webserver doesn't support hijacking", http.StatusInternalServerError)
		return
	}

	conn, bufrw, err := hj.Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer conn.Close()

	// Write HTTP 101 Switching Protocols
	bufrw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: herd-exec\r\n\r\n")
	bufrw.Flush()

	// Dial Firecracker vsock:5001
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	vsockConn, err := vsock.DialFirecracker(ctx, socketPath, 5001)
	if err != nil {
		fmt.Fprintf(conn, "failed to dial vsock: %v\n", err)
		return
	}
	defer vsockConn.Close()

	errc := make(chan error, 2)
	go func() {
		_, err := io.Copy(conn, vsockConn)
		errc <- err
	}()
	go func() {
		_, err := io.Copy(vsockConn, conn)
		errc <- err
	}()

	<-errc
}
