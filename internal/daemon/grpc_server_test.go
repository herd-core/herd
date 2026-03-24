package daemon

import (
	"context"
	"io"
	"net"
	"testing"

	pb "github.com/herd-core/herd/proto/herd/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

const bufSize = 1024 * 1024

var lis *bufconn.Listener

func initGRPCServer() {
	lis = bufconn.Listen(bufSize)
	s := grpc.NewServer()
	pb.RegisterHerdServiceServer(s, NewServer(nil, "http://127.0.0.1:4000", 10))
	go func() {
		if err := s.Serve(lis); err != nil {
			panic(err)
		}
	}()
}

func bufDialer(context.Context, string) (net.Conn, error) {
	return lis.Dial()
}

func TestAcquireStream(t *testing.T) {
	initGRPCServer()
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
}
