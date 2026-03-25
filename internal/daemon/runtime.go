package daemon

import (
	"fmt"
	"net"
	"net/http"
	"os"

	"github.com/herd-core/herd"
	"github.com/herd-core/herd/internal/lifecycle"
	"github.com/herd-core/herd/proxy"
)

const SessionHeader = "X-Session-ID"

// ListenUnixSocket creates a UDS listener for the control plane.
// It removes stale socket files before binding and applies owner-only perms.
func ListenUnixSocket(path string) (net.Listener, error) {
	if err := RemoveUnixSocket(path); err != nil {
		return nil, fmt.Errorf("remove stale socket: %w", err)
	}

	lis, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on unix socket %q: %w", path, err)
	}

	if err := os.Chmod(path, 0o600); err != nil {
		_ = lis.Close()
		return nil, fmt.Errorf("chmod socket %q: %w", path, err)
	}

	return lis, nil
}

// RemoveUnixSocket removes a control-plane UDS file if it exists.
func RemoveUnixSocket(path string) error {
	err := os.Remove(path)
	if err == nil || os.IsNotExist(err) {
		return nil
	}
	return err
}

// NewDataPlaneHandler builds the HTTP data-plane mux.
// - Proxy traffic is routed by X-Session-ID to session-affine workers.
// - /healthz reports daemon liveness.
// - telemetry metrics are exposed at metricsPath.
func NewDataPlaneHandler(pool *herd.Pool[*http.Client], lm *lifecycle.Manager, metricsPath string) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	mux.Handle(metricsPath, MetricsHandler(pool))

	mux.Handle("/", proxy.NewReverseProxy(pool, func(r *http.Request) string {
		return r.Header.Get(SessionHeader)
	}).WithLifecycleManager(lm).WithLookupOnly())

	return mux
}

// MetricsHandler exposes a small Prometheus-compatible text endpoint.
func MetricsHandler(pool *herd.Pool[*http.Client]) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		stats := pool.Stats()
		life := SnapshotLifecycleCounters()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

		_, _ = fmt.Fprintf(w,
			"# HELP herd_total_workers Total workers tracked by herd pool.\n"+
				"# TYPE herd_total_workers gauge\n"+
				"herd_total_workers %d\n"+
				"# HELP herd_available_workers Workers currently idle and available.\n"+
				"# TYPE herd_available_workers gauge\n"+
				"herd_available_workers %d\n"+
				"# HELP herd_active_sessions Active sticky sessions.\n"+
				"# TYPE herd_active_sessions gauge\n"+
				"herd_active_sessions %d\n"+
				"# HELP herd_sessions_draining Sessions in grace period awaiting reconnect.\n"+
				"# TYPE herd_sessions_draining gauge\n"+
				"herd_sessions_draining %d\n"+
				"# HELP herd_inflight_acquires Acquire operations in progress.\n"+
				"# TYPE herd_inflight_acquires gauge\n"+
				"herd_inflight_acquires %d\n"+
				"# HELP herd_acquire_requests_total Total control-plane acquire requests received.\n"+
				"# TYPE herd_acquire_requests_total counter\n"+
				"herd_acquire_requests_total %d\n"+
				"# HELP herd_acquire_failures_total Total acquire requests that failed.\n"+
				"# TYPE herd_acquire_failures_total counter\n"+
				"herd_acquire_failures_total %d\n"+
				"# HELP herd_sessions_started_total Total sessions successfully started by control-plane streams.\n"+
				"# TYPE herd_sessions_started_total counter\n"+
				"herd_sessions_started_total %d\n"+
				"# HELP herd_sessions_killed_total Total sessions force-killed during stream cleanup.\n"+
				"# TYPE herd_sessions_killed_total counter\n"+
				"herd_sessions_killed_total %d\n",
			stats.TotalWorkers,
			stats.AvailableWorkers,
			stats.ActiveSessions,
			life.SessionsDraining,
			stats.InflightAcquires,
			life.AcquireRequests,
			life.AcquireFailures,
			life.SessionsStarted,
			life.SessionsKilled,
		)
	})
}
