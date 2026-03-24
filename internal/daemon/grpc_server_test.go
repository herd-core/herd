package daemon

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/herd-core/herd"
	pb "github.com/herd-core/herd/proto/herd/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

const bufSize = 1024 * 1024

var lis *bufconn.Listener

func initGRPCServer(t *testing.T, srv pb.HerdServiceServer) {
	t.Helper()
	lis = bufconn.Listen(bufSize)
	s := grpc.NewServer()
	pb.RegisterHerdServiceServer(s, srv)
	go func() {
		if err := s.Serve(lis); err != nil {
			panic(err)
		}
	}()

	t.Cleanup(func() {
		s.Stop()
		_ = lis.Close()
	})
}

func bufDialer(context.Context, string) (net.Conn, error) {
	return lis.Dial()
}

func TestAcquireStream(t *testing.T) {
	factory := &testFactory{workers: []*testWorker{{id: "w1"}}}
	pool, err := herd.New[*http.Client](factory, herd.WithAutoScale(1, 1))
	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}
	t.Cleanup(func() {
		_ = pool.Shutdown(context.Background())
	})

	initGRPCServer(t, NewServer(pool, "http://127.0.0.1:4000", 1, NewEventLogger("text", nil)))

	ctx := context.Background()
	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(bufDialer), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("Failed to dial bufnet: %v", err)
	}
	defer conn.Close()

	client := pb.NewHerdServiceClient(conn)
	stream, err := client.Acquire(ctx)
	if err != nil {
		t.Fatalf("Failed to open Acquire stream: %v", err)
	}

	req := &pb.AcquireRequest{WorkerType: "python"}
	if err := stream.Send(req); err != nil {
		t.Fatalf("Failed to send request: %v", err)
	}

	resp, err := stream.Recv()
	if err != nil {
		t.Fatalf("Failed to receive response: %v", err)
	}

	if resp.GetSessionId() == "" {
		t.Error("Expected non-empty session_id")
	}
	if got := resp.GetProxyAddress(); got != "http://127.0.0.1:4000" {
		t.Errorf("Expected proxy_address http://127.0.0.1:4000, got %s", got)
	}

	// Close stream
	if err := stream.CloseSend(); err != nil {
		t.Fatalf("Failed to close stream: %v", err)
	}

	// Wait for server to finish stream
	_, err = stream.Recv()
	if err != io.EOF {
		t.Errorf("Expected EOF after close, got %v", err)
	}

	if !waitForClosed(factory.workers[0], time.Second) {
		t.Fatal("expected worker to be force-closed after stream EOF")
	}
}

func TestAcquireStream_WithoutPool(t *testing.T) {
	initGRPCServer(t, NewServer(nil, "http://127.0.0.1:4000", 1, NewEventLogger("text", nil)))

	ctx := context.Background()
	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(bufDialer), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("Failed to dial bufnet: %v", err)
	}
	defer conn.Close()

	client := pb.NewHerdServiceClient(conn)
	stream, err := client.Acquire(ctx)
	if err != nil {
		t.Fatalf("Failed to open Acquire stream: %v", err)
	}

	if err := stream.Send(&pb.AcquireRequest{WorkerType: "python"}); err != nil {
		t.Fatalf("Failed to send request: %v", err)
	}

	_, err = stream.Recv()
	if err == nil {
		t.Fatal("expected recv error when pool is not configured")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %s", st.Code())
	}
}

type testWorker struct {
	id string

	mu     sync.Mutex
	closed bool

	onCrash func(string)
}

func (w *testWorker) ID() string                    { return w.id }
func (w *testWorker) Address() string               { return "http://127.0.0.1:19000" }
func (w *testWorker) Client() *http.Client          { return &http.Client{} }
func (w *testWorker) Healthy(context.Context) error { return nil }
func (w *testWorker) OnCrash(fn func(string))       { w.onCrash = fn }
func (w *testWorker) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	return nil
}

func (w *testWorker) isClosed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closed
}

type testFactory struct {
	mu      sync.Mutex
	workers []*testWorker
	idx     int
}

func (f *testFactory) Spawn(context.Context) (herd.Worker[*http.Client], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.idx >= len(f.workers) {
		// Return a closed-over worker to keep the pool alive in tests where
		// maybeScaleUp can be triggered after KillSession.
		w := &testWorker{id: "fallback"}
		return w, nil
	}
	w := f.workers[f.idx]
	f.idx++
	return w, nil
}

func waitForClosed(w *testWorker, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if w.isClosed() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return w.isClosed()
}
