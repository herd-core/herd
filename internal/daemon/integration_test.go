package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/herd-core/herd"
	"github.com/herd-core/herd/internal/lifecycle"
)

func TestDaemonIntegration_AcquireProxyAndCleanup(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("upstream-ok"))
	}))
	defer backend.Close()

	factory := &integrationFactory{upstreamAddress: backend.URL}
	pool, err := herd.New[*http.Client](factory, herd.WithAutoScale(1, 1), herd.WithTTL(5*time.Minute))
	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}
	defer func() { _ = pool.Shutdown(context.Background()) }()

	lm := lifecycle.NewManager(lifecycle.Config{AbsoluteTTL: 5 * time.Minute}, pool)
	go lm.StartReaper(context.Background())

	dataPlane := httptest.NewServer(NewDataPlaneHandler(pool, lm, "/metrics"))
	defer dataPlane.Close()

	controlPlaneHandler := NewControlPlaneHandler(pool, lm, dataPlane.URL, NewEventLogger("text", nil))
	controlPlane := httptest.NewServer(controlPlaneHandler)
	defer controlPlane.Close()

	// Perform POST /v1/sessions
	reqBody := `{"image": "test", "idle_timeout_seconds": 300}`
	resp, err := http.Post(controlPlane.URL+"/v1/sessions", "application/json", bytes.NewBufferString(reqBody))
	if err != nil {
		t.Fatalf("failed to post /v1/sessions: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	var createResp SessionCreateResponse
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	sessionID := createResp.SessionID
	if sessionID == "" {
		t.Fatal("expected non-empty session id")
	}

	// Test proxy access
	dpReq, err := http.NewRequest(http.MethodGet, createResp.ProxyAddress+"/", nil)
	if err != nil {
		t.Fatalf("build data-plane request: %v", err)
	}
	dpReq.Header.Set(SessionHeader, sessionID)

	httpResp, err := http.DefaultClient.Do(dpReq)
	if err != nil {
		t.Fatalf("data-plane request failed: %v", err)
	}
	body, _ := io.ReadAll(httpResp.Body)
	httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from data-plane, got %d", httpResp.StatusCode)
	}
	if string(body) != "upstream-ok" {
		t.Fatalf("unexpected data-plane body: %q", string(body))
	}

	// Wait for cleanup by performing DELETE
	delReq, _ := http.NewRequest(http.MethodDelete, controlPlane.URL+"/v1/sessions/"+sessionID, nil)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatalf("failed to delete session: %v", err)
	}
	delResp.Body.Close()

	first := factory.firstWorker()
	if first == nil {
		t.Fatal("expected first worker to be available for cleanup assertion")
	}
	if !waitClosed(first, time.Second) {
		t.Fatal("expected first worker to be closed after explicit delete")
	}
}

func TestDaemonIntegration_StatusHealthAndMetrics(t *testing.T) {
	factory := &integrationFactory{upstreamAddress: "http://127.0.0.1:1"}
	pool, err := herd.New[*http.Client](factory, herd.WithAutoScale(1, 1), herd.WithTTL(5*time.Minute))
	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}
	defer func() { _ = pool.Shutdown(context.Background()) }()

	lm := lifecycle.NewManager(lifecycle.Config{AbsoluteTTL: 5 * time.Minute}, pool)
	go lm.StartReaper(context.Background())

	dataPlane := httptest.NewServer(NewDataPlaneHandler(pool, lm, "/metrics"))
	defer dataPlane.Close()

	// We no longer have Status gRPC call. We can just test Health and Metrics.
	healthResp, err := http.Get(dataPlane.URL + "/healthz")
	if err != nil {
		t.Fatalf("healthz request failed: %v", err)
	}
	healthBody, _ := io.ReadAll(healthResp.Body)
	healthResp.Body.Close()
	if healthResp.StatusCode != http.StatusOK || string(healthBody) != "ok" {
		t.Fatalf("unexpected healthz response: status=%d body=%q", healthResp.StatusCode, string(healthBody))
	}

	metricsResp, err := http.Get(dataPlane.URL + "/metrics")
	if err != nil {
		t.Fatalf("metrics request failed: %v", err)
	}
	metricsBody, _ := io.ReadAll(metricsResp.Body)
	metricsResp.Body.Close()
	metrics := string(metricsBody)

	if metricsResp.StatusCode != http.StatusOK {
		t.Fatalf("expected metrics status 200, got %d", metricsResp.StatusCode)
	}
	if !strings.Contains(metrics, "herd_total_workers") {
		t.Fatalf("expected herd_total_workers in metrics output: %s", metrics)
	}
	if !strings.Contains(metrics, "herd_acquire_requests_total") {
		t.Fatalf("expected lifecycle counters in metrics output: %s", metrics)
	}
}

type integrationFactory struct {
	upstreamAddress string
	seq             atomic.Uint64

	mu    sync.Mutex
	first *integrationWorker
}

func (f *integrationFactory) Spawn(context.Context) (herd.Worker[*http.Client], error) {
	w := &integrationWorker{id: fmt.Sprintf("it-worker-%d", f.seq.Add(1)), address: f.upstreamAddress}

	f.mu.Lock()
	if f.first == nil {
		f.first = w
	}
	f.mu.Unlock()

	return w, nil
}

func (f *integrationFactory) firstWorker() *integrationWorker {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.first
}

type integrationWorker struct {
	id      string
	address string

	mu     sync.Mutex
	closed bool
}

func (w *integrationWorker) ID() string                    { return w.id }
func (w *integrationWorker) Address() string               { return w.address }
func (w *integrationWorker) Client() *http.Client          { return &http.Client{} }
func (w *integrationWorker) Healthy(context.Context) error { return nil }
func (w *integrationWorker) OnCrash(func(string))          {}

func (w *integrationWorker) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	return nil
}

func waitClosed(w *integrationWorker, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		w.mu.Lock()
		closed := w.closed
		w.mu.Unlock()
		if closed {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closed
}
