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

type ControlPlaneAPI struct {
	cntrl		*Controller
}

func NewControlPlaneHandler(controller *Controller) http.Handler {
	api := ControlPlaneAPI{
		cntrl: controller,
	}
	
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", api.handleHealthCheck)
	mux.HandleFunc("POST /v1/sessions", api.handleCreateSession)
	mux.HandleFunc("GET /v1/sessions", api.handleListSessions)
	mux.HandleFunc("DELETE /v1/sessions/{id}", api.handleDeleteSession) // /v1/sessions/{id}
	mux.HandleFunc("GET /v1/sessions/{id}/logs", api.handleLogsSession)      // /v1/sessions/{id}/logs
	mux.HandleFunc("POST /v1/sessions/{id}/exec", api.handleExecSession)     // /v1/sessions/{id}/exec
	mux.HandleFunc("PUT /v1/sessions/{id}/heartbeat", api.handleHeartbeat)        // /v1/sessions/{id}/heartbeat

	mux.HandleFunc("POST /v1/images/warm", api.handleWarmImage)
	
	return mux
}

func (api *ControlPlaneAPI) handleHealthCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte("OK"))
}

func (api *ControlPlaneAPI) handleWarmImage(w http.ResponseWriter, r *http.Request) {
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

	if err := api.cntrl.WarmImage(r.Context(), req.Image); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (api *ControlPlaneAPI) handleLogsSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if sessionID == "" {
		http.Error(w, "invalid session id", http.StatusBadRequest)
		return
	}

	readCloser, err := api.cntrl.GetLogs(r.Context(), sessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer readCloser.Close()

	w.Header().Set("Content-Type", "text/plain")
	if _, err := io.Copy(w, readCloser); err != nil {
		api.cntrl.logger.Error("failed_to_copy_logs_to_response", map[string]any{"error": err, "session_id": sessionID})
	}
}

func (api *ControlPlaneAPI) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if sessionID == "" {
		http.Error(w, "invalid session id", http.StatusBadRequest)
		return
	}

	api.cntrl.UpdateHeartbeat(r.Context(), sessionID)
	w.WriteHeader(http.StatusOK)
}

func (api *ControlPlaneAPI) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	if r.Body == nil {
		http.Error(w, "missing request body", http.StatusBadRequest)
		return
	}

	var req SessionCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.cntrl.logger.Error("failed_to_decode_create_request", map[string]any{"error": err})
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}

	if req.Image == "" {
		http.Error(w, "missing image field", http.StatusBadRequest)
		return
	}

	resp, err := api.cntrl.CreateSession(r.Context(), req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		api.cntrl.logger.Error("failed_to_encode_create_response", map[string]any{"error": err, "session_id": resp.SessionID})
	}
}

func (api *ControlPlaneAPI) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if sessionID == "" {
		http.Error(w, "invalid session id", http.StatusBadRequest)
		return
	}

	err := api.cntrl.DeleteSession(r.Context(), sessionID, "api_requested")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (api *ControlPlaneAPI) handleListSessions(w http.ResponseWriter, r *http.Request) {
	sessions := api.cntrl.ListSessions(r.Context())

	data, err := json.Marshal(sessions)
	if err != nil {
		api.cntrl.logger.Error("failed_to_encode_sessions_list", map[string]any{"error": err})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(data); err != nil {
		api.cntrl.logger.Error("failed_to_write_sessions_list", map[string]any{"error": err})
	}
}

func (api *ControlPlaneAPI) handleExecSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if sessionID == "" {
		http.Error(w, "invalid session id", http.StatusBadRequest)
		return
	}

	session, err := api.cntrl.pool.GetSession(r.Context(), sessionID)
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
			api.cntrl.logger.Error("failed_to_close_hijacked_conn", map[string]any{"error": cerr, "session_id": sessionID})
		}
	}()

	// Write HTTP 101 Switching Protocols
	if _, err := bufrw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: herd-exec\r\n\r\n"); err != nil {
		api.cntrl.logger.Error("failed_to_write_exec_upgrade_header", map[string]any{"error": err, "session_id": sessionID})
		return
	}
	if err := bufrw.Flush(); err != nil {
		api.cntrl.logger.Error("failed_to_flush_exec_upgrade_header", map[string]any{"error": err, "session_id": sessionID})
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
			api.cntrl.logger.Error("failed_to_close_vsock_conn", map[string]any{"error": cerr, "session_id": sessionID})
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
