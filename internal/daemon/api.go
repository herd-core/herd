package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/herd-core/herd/internal/vsock"
)

type ControlPlaneHandler struct {
	controller *Controller
}

func NewControlPlaneHandler(controller *Controller) http.Handler {
	h := &ControlPlaneHandler{
		controller: controller,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions", h.handleCreateSession)
	mux.HandleFunc("GET /v1/sessions", h.handleListSessions)
	mux.HandleFunc("DELETE /v1/sessions/", h.handleDeleteSession) // /v1/sessions/{id}
	mux.HandleFunc("GET /v1/sessions/", h.handleLogsSession)      // /v1/sessions/{id}/logs
	mux.HandleFunc("POST /v1/sessions/", h.handleExecSession)     // /v1/sessions/{id}/exec
	mux.HandleFunc("PUT /v1/sessions/", h.handleHeartbeat)        // /v1/sessions/{id}/heartbeat

	mux.HandleFunc("POST /v1/images/warm", h.handleWarmImage)
	
	return mux
}

func (h *ControlPlaneHandler) handleWarmImage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Image string `json:"image"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if req.Image == "" {
		http.Error(w, "image required", http.StatusBadRequest)
		return
	}

	if err := h.controller.WarmImage(r.Context(), req.Image); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *ControlPlaneHandler) handleLogsSession(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 4 || parts[3] != "logs" {
		http.Error(w, "invalid path", http.StatusNotFound)
		return
	}
	sessionID := parts[2]

	readCloser, err := h.controller.GetLogs(r.Context(), sessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer readCloser.Close()

	w.Header().Set("Content-Type", "text/plain")
	if _, err := io.Copy(w, readCloser); err != nil {
		h.controller.logger.Error("failed_to_copy_logs_to_response", map[string]any{"error": err, "session_id": sessionID})
	}
}

func (h *ControlPlaneHandler) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 4 || parts[3] != "heartbeat" {
		http.Error(w, "invalid path", http.StatusNotFound)
		return
	}
	sessionID := parts[2]
	h.controller.UpdateHeartbeat(r.Context(), sessionID)
	w.WriteHeader(http.StatusOK)
}

func (h *ControlPlaneHandler) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req SessionCreateRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			h.controller.logger.Error("failed_to_decode_create_request", map[string]any{"error": err})
			http.Error(w, "invalid json body", http.StatusBadRequest)
			return
		}
	}

	resp, err := h.controller.CreateSession(r.Context(), req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		h.controller.logger.Error("failed_to_encode_create_response", map[string]any{"error": err, "session_id": resp.SessionID})
	}
}

func (h *ControlPlaneHandler) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	// Extract sessionID from URL: /v1/sessions/{id}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 3 {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	sessionID := parts[2]

	err := h.controller.DeleteSession(r.Context(), sessionID, "api_requested")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (h *ControlPlaneHandler) handleListSessions(w http.ResponseWriter, r *http.Request) {
	sessions := h.controller.ListSessions(r.Context())

	data, err := json.Marshal(sessions)
	if err != nil {
		h.controller.logger.Error("failed_to_encode_sessions_list", map[string]any{"error": err})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(data); err != nil {
		h.controller.logger.Error("failed_to_write_sessions_list", map[string]any{"error": err})
	}
}

func (h *ControlPlaneHandler) handleExecSession(w http.ResponseWriter, r *http.Request) {
	// Path: /v1/sessions/{id}/exec
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 4 || parts[3] != "exec" {
		http.Error(w, "invalid path", http.StatusNotFound)
		return
	}
	sessionID := parts[2]

	session, err := h.controller.pool.GetSession(r.Context(), sessionID)
	if err != nil || session == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	var socketPath string
	if fv, ok := session.Worker.(interface{ VsockUDSPath() string }); ok {
		socketPath = fv.VsockUDSPath()
	} else {
		addr := session.Worker.Address()
		if !strings.HasPrefix(addr, "unix://") {
			http.Error(w, "worker does not support local vsock exec", http.StatusBadRequest)
			return
		}
		socketPath = strings.TrimPrefix(addr, "unix://")
	}
	if socketPath == "" {
		http.Error(w, "worker vsock path unavailable", http.StatusBadRequest)
		return
	}

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
	defer func() {
		if cerr := conn.Close(); cerr != nil {
			h.controller.logger.Error("failed_to_close_hijacked_conn", map[string]any{"error": cerr, "session_id": sessionID})
		}
	}()

	// Write HTTP 101 Switching Protocols
	if _, err := bufrw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: herd-exec\r\n\r\n"); err != nil {
		h.controller.logger.Error("failed_to_write_exec_upgrade_header", map[string]any{"error": err, "session_id": sessionID})
		return
	}
	if err := bufrw.Flush(); err != nil {
		h.controller.logger.Error("failed_to_flush_exec_upgrade_header", map[string]any{"error": err, "session_id": sessionID})
		return
	}

	// Dial Firecracker vsock:5001
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	vsockConn, err := vsock.DialFirecracker(ctx, socketPath, 5001)
	if err != nil {
		_, _ = fmt.Fprintf(conn, "failed to dial vsock: %v\n", err)
		return
	}
	defer func() {
		if cerr := vsockConn.Close(); cerr != nil {
			h.controller.logger.Error("failed_to_close_vsock_conn", map[string]any{"error": cerr, "session_id": sessionID})
		}
	}()

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
