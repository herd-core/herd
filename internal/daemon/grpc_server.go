package daemon

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/herd-core/herd"
	pb "github.com/herd-core/herd/proto/herd/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Server implements the HerdService gRPC server.
type Server struct {
	pb.UnimplementedHerdServiceServer
	pool         *herd.Pool[*http.Client]
	proxyAddress string
	maxWorkers   int
	seq          atomic.Uint64
	logger       *EventLogger
}

func NewServer(pool *herd.Pool[*http.Client], proxyAddress string, maxWorkers int, logger *EventLogger) *Server {
	return &Server{pool: pool, proxyAddress: proxyAddress, maxWorkers: maxWorkers, logger: logger}
}

// Acquire handles bidirectional streaming allocation.
func (s *Server) Acquire(stream pb.HerdService_AcquireServer) error {
	if s.pool == nil {
		return status.Error(codes.FailedPrecondition, "daemon server is not configured with a pool")
	}

	var session *herd.Session[*http.Client]

	defer func() {
		if session == nil {
			return
		}
		if err := s.pool.KillSession(session.ID); err != nil {
			s.eventLogger().Error("session_kill_failed", map[string]any{"session_id": session.ID, "error": err})
			return
		}
		RecordSessionKilled()
		s.eventLogger().Info("session_killed", map[string]any{"session_id": session.ID})
	}()

	for {
		req, err := stream.Recv()
		if err == io.EOF {
			s.eventLogger().Info("acquire_stream_closed", map[string]any{})
			return nil // Client closed gracefully
		}
		if err != nil {
			s.eventLogger().Warn("acquire_stream_broken", map[string]any{"error": err})
			return err // Stream broken
		}
		RecordAcquireRequest()

		s.eventLogger().Info("acquire_request_received", map[string]any{"worker_type": req.GetWorkerType()})
		if session == nil {
			sessionID := fmt.Sprintf("sess-%d", s.seq.Add(1))
			acquireCtx := stream.Context()
			if req.GetTimeoutSeconds() > 0 {
				var cancel context.CancelFunc
				acquireCtx, cancel = context.WithTimeout(stream.Context(), time.Duration(req.GetTimeoutSeconds())*time.Second)
				session, err = s.pool.Acquire(acquireCtx, sessionID)
				cancel()
			} else {
				session, err = s.pool.Acquire(acquireCtx, sessionID)
			}
			if err != nil {
				RecordAcquireFailure()
				s.eventLogger().Error("session_acquire_failed", map[string]any{"session_id": sessionID, "error": err})
				return status.Errorf(codes.ResourceExhausted, "acquire session failed: %v", err)
			}
			RecordSessionStarted()
			s.eventLogger().Info("session_acquired", map[string]any{"session_id": session.ID})
		}

		resp := &pb.AcquireResponse{
			SessionId:    session.ID,
			ProxyAddress: s.proxyAddress,
			WorkerPid:    0,
		}

		if err := stream.Send(resp); err != nil {
			s.eventLogger().Warn("acquire_response_send_failed", map[string]any{"session_id": session.ID, "error": err})
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

func (s *Server) eventLogger() *EventLogger {
	if s.logger == nil {
		s.logger = NewEventLogger("text", nil)
	}
	return s.logger
}
