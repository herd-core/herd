package daemon

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync/atomic"

	"github.com/herd-core/herd"
	pb "github.com/herd-core/herd/proto/herd/v1"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Server implements the HerdService gRPC server.
type Server struct {
	pb.UnimplementedHerdServiceServer
	pool         *herd.Pool[*http.Client]
	proxyAddress string
	maxWorkers   int
	seq          atomic.Uint64
}

func NewServer(pool *herd.Pool[*http.Client], proxyAddress string, maxWorkers int) *Server {
	return &Server{pool: pool, proxyAddress: proxyAddress, maxWorkers: maxWorkers}
}

// Acquire handles bidirectional streaming allocation.
func (s *Server) Acquire(stream pb.HerdService_AcquireServer) error {
	for {
		req, err := stream.Recv()
		if err == io.EOF {
			return nil // Client closed gracefully
		}
		if err != nil {
			return err // Stream broken
		}

		log.Printf("Received Acquire Request for worker_type: %s", req.GetWorkerType())

		sessionID := fmt.Sprintf("sess-%d", s.seq.Add(1))
		resp := &pb.AcquireResponse{
			SessionId:    sessionID,
			ProxyAddress: s.proxyAddress,
			WorkerPid:    0,
		}

		if err := stream.Send(resp); err != nil {
			return err
		}
	}
}

// Status returns daemon health metrics.
func (s *Server) Status(ctx context.Context, _ *emptypb.Empty) (*pb.StatusResponse, error) {
	if s.pool == nil {
		return &pb.StatusResponse{
			ActiveWorkers: 0,
			IdleWorkers:   0,
			MaxWorkers:    int32(s.maxWorkers),
		}, nil
	}

	stats := s.pool.Stats()
	return &pb.StatusResponse{
		ActiveWorkers: int32(stats.ActiveSessions),
		IdleWorkers:   int32(stats.AvailableWorkers),
		MaxWorkers:    int32(s.maxWorkers),
	}, nil
}
