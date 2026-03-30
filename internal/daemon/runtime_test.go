package daemon

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/herd-core/herd"
)

func TestListenUnixSocket_RemovesStaleFile(t *testing.T) {
	t.Parallel()

	path := fmt.Sprintf("/tmp/herd-%d.sock", time.Now().UnixNano())
	_ = os.Remove(path)
	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatalf("write stale file: %v", err)
	}

	lis, err := ListenUnixSocket(path)
	if err != nil {
		t.Fatalf("ListenUnixSocket returned error: %v", err)
	}
	t.Cleanup(func() {
		_ = lis.Close()
		_ = RemoveUnixSocket(path)
	})

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if perms := fi.Mode().Perm(); perms != 0o600 {
		t.Fatalf("expected socket perms 0600, got %o", perms)
	}
}

func TestNewDataPlaneHandler_Healthz(t *testing.T) {
	t.Parallel()

	h := NewDataPlaneHandler(nil, nil, "/metrics")
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body, _ := io.ReadAll(rr.Body)
	if string(body) != "ok" {
		t.Fatalf("expected body 'ok', got %q", string(body))
	}
}

func TestMetricsHandler_ContainsLifecycleCounters(t *testing.T) {
	t.Parallel()

	RecordAcquireRequest()
	RecordAcquireFailure()
	RecordSessionStarted()
	RecordSessionKilled()

	pool := newMetricsPool(t)
	h := MetricsHandler(pool)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body, _ := io.ReadAll(rr.Body)
	b := string(body)
	if !strings.Contains(b, "herd_acquire_requests_total") {
		t.Fatalf("expected acquire counter in metrics output, got: %s", b)
	}
	if !strings.Contains(b, "herd_sessions_killed_total") {
		t.Fatalf("expected kill counter in metrics output, got: %s", b)
	}
}

type metricsWorker struct{ id string }

func (w *metricsWorker) ID() string                    { return w.id }
func (w *metricsWorker) Address() string               { return "http://127.0.0.1:19001" }
func (w *metricsWorker) Client() *http.Client          { return &http.Client{} }
func (w *metricsWorker) Healthy(context.Context) error { return nil }
func (w *metricsWorker) OnCrash(func(string))          {}
func (w *metricsWorker) Close() error                  { return nil }

type metricsFactory struct{}

func (f *metricsFactory) Spawn(_ context.Context, _ string, _ herd.TenantConfig) (herd.Worker[*http.Client], error) {
	return &metricsWorker{id: "m1"}, nil
}

func newMetricsPool(t *testing.T) *herd.Pool[*http.Client] {
	t.Helper()
	p, err := herd.New[*http.Client](&metricsFactory{}, herd.WithMaxWorkers(1))
	if err != nil {
		t.Fatalf("failed creating metrics pool: %v", err)
	}
	t.Cleanup(func() {
		_ = p.Shutdown(context.Background())
	})
	return p
}
