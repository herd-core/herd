package daemon

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/herd-core/herd"
	pb "github.com/herd-core/herd/proto/herd/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/emptypb"
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

	dataPlane := httptest.NewServer(NewDataPlaneHandler(pool, "/metrics"))
	defer dataPlane.Close()

	socketPath := fmt.Sprintf("/tmp/herd-it-%d.sock", time.Now().UnixNano())
	grpcServer, cleanup := startIntegrationGRPCServer(t, socketPath, NewServer(pool, dataPlane.URL, 1, NewEventLogger("text", nil)))
	defer cleanup()
	defer grpcServer.Stop()

	conn, err := dialUnixGRPC(socketPath)
	if err != nil {
		t.Fatalf("failed to dial control plane: %v", err)
	}
	defer conn.Close()

	client := pb.NewHerdServiceClient(conn)
	stream, err := client.Acquire(context.Background())
	if err != nil {
		t.Fatalf("failed to open acquire stream: %v", err)
	}

	if err := stream.Send(&pb.AcquireRequest{WorkerType: "test"}); err != nil {
		t.Fatalf("failed to send acquire request: %v", err)
	}

	resp, err := stream.Recv()
	if err != nil {
		t.Fatalf("failed to receive acquire response: %v", err)
	}
	if resp.GetSessionId() == "" {
		t.Fatal("expected non-empty session id")
	}
	if got := resp.GetProxyAddress(); got != dataPlane.URL {
		t.Fatalf("expected proxy address %q, got %q", dataPlane.URL, got)
	}

	req, err := http.NewRequest(http.MethodGet, resp.GetProxyAddress()+"/", nil)
	if err != nil {
		t.Fatalf("build data-plane request: %v", err)
	}
	req.Header.Set(SessionHeader, resp.GetSessionId())

	httpResp, err := http.DefaultClient.Do(req)
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

	if err := stream.CloseSend(); err != nil {
		t.Fatalf("close stream failed: %v", err)
	}
	_, err = stream.Recv()
	if err != io.EOF {
		t.Fatalf("expected EOF after CloseSend, got %v", err)
	}

	first := factory.firstWorker()
	if first == nil {
		t.Fatal("expected first worker to be available for cleanup assertion")
	}
	if !waitClosed(first, time.Second) {
		t.Fatal("expected first worker to be closed after control stream EOF")
	}
}

func TestDaemonIntegration_StatusHealthAndMetrics(t *testing.T) {
	factory := &integrationFactory{upstreamAddress: "http://127.0.0.1:1"}
	pool, err := herd.New[*http.Client](factory, herd.WithAutoScale(1, 1), herd.WithTTL(5*time.Minute))
	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}
	defer func() { _ = pool.Shutdown(context.Background()) }()

	dataPlane := httptest.NewServer(NewDataPlaneHandler(pool, "/metrics"))
	defer dataPlane.Close()

	socketPath := fmt.Sprintf("/tmp/herd-it-%d.sock", time.Now().UnixNano())
	grpcServer, cleanup := startIntegrationGRPCServer(t, socketPath, NewServer(pool, dataPlane.URL, 1, NewEventLogger("text", nil)))
	defer cleanup()
	defer grpcServer.Stop()

	conn, err := dialUnixGRPC(socketPath)
	if err != nil {
		t.Fatalf("failed to dial control plane: %v", err)
	}
	defer conn.Close()

	client := pb.NewHerdServiceClient(conn)
	statusResp, err := client.Status(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("status rpc failed: %v", err)
	}
	if statusResp.GetMaxWorkers() != 1 {
		t.Fatalf("expected max_workers=1, got %d", statusResp.GetMaxWorkers())
	}

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

func startIntegrationGRPCServer(t *testing.T, socketPath string, srv pb.HerdServiceServer) (*grpc.Server, func()) {
	t.Helper()

	lis, err := ListenUnixSocket(socketPath)
	if err != nil {
		t.Fatalf("listen unix socket failed: %v", err)
	}

	s := grpc.NewServer()
	pb.RegisterHerdServiceServer(s, srv)

	go func() {
		if err := s.Serve(lis); err != nil && !strings.Contains(err.Error(), "use of closed network connection") {
			// Best-effort safety in tests: fail hard on unexpected serve errors.
			panic(err)
		}
	}()

	cleanup := func() {
		_ = lis.Close()
		_ = RemoveUnixSocket(socketPath)
	}

	return s, cleanup
}

func dialUnixGRPC(socketPath string) (*grpc.ClientConn, error) {
	return grpc.NewClient(
		"passthrough:///herd-integration",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return net.Dial("unix", socketPath)
		}),
	)
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
