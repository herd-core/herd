package daemon

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/herd-core/herd"
	"github.com/herd-core/herd/internal/lifecycle"
	pb "github.com/herd-core/herd/proto/herd/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Server implements the HerdService gRPC server.
type Server struct {
	pb.UnimplementedHerdServiceServer
	pool             *herd.Pool[*http.Client]
	lifecycleManager *lifecycle.Manager
	proxyAddress     string
	maxWorkers       int
	seq              atomic.Uint64
	logger           *EventLogger
}

func NewServer(
	pool *herd.Pool[*http.Client],
	lifecycleManager *lifecycle.Manager,
	proxyAddress string,
	maxWorkers int,
	logger *EventLogger,
) *Server {
	return &Server{
		pool:             pool,
		lifecycleManager: lifecycleManager,
		proxyAddress:     proxyAddress,
		maxWorkers:       maxWorkers,
		logger:           logger,
	}
}

// Acquire handles bidirectional streaming allocation.
//
// New Flow:
// 1. Handshake & ID Generation/Re-use.
// 2. Pool Acquisition.
// 3. Lifecycle Registration.
// 4. Heartbeat Loop.
// 5. Cleanup on Exit.
func (s *Server) Acquire(stream pb.HerdService_AcquireServer) error {
	if s.pool == nil {
		return status.Error(codes.FailedPrecondition, "daemon server is not configured with a pool")
	}

	// 1. Handshake
	req, err := stream.Recv()
	if err == io.EOF {
		return nil
	}
	if err != nil {
		return err
	}
	RecordAcquireRequest()

	sessionID := req.GetSessionId()
	if sessionID == "" {
		sessionID = fmt.Sprintf("sess-%d", s.seq.Add(1))
	}
	s.eventLogger().Info("acquire_request_received", map[string]any{"worker_type": req.GetWorkerType(), "session_id": sessionID})

	acquireCtx := stream.Context()
	if req.GetTimeoutSeconds() > 0 {
		var cancelAcquire context.CancelFunc
		acquireCtx, cancelAcquire = context.WithTimeout(stream.Context(), time.Duration(req.GetTimeoutSeconds())*time.Second)
		defer cancelAcquire()
	}

	// 2. Pool Acquisition
	session, acquireErr := s.pool.Acquire(acquireCtx, sessionID)
	if acquireErr != nil {
		RecordAcquireFailure()
		s.eventLogger().Error("session_acquire_failed", map[string]any{"session_id": sessionID, "error": acquireErr})
		return status.Errorf(codes.ResourceExhausted, "acquire session failed: %v", acquireErr)
	}

	// 3. Lifecycle Registration
	s.lifecycleManager.Register(sessionID)
	// Clean up on exit: ensures worker is killed if client disconnects.
	defer func() {
		err := s.lifecycleManager.UnregisterAndKill(sessionID, "client_disconnected")
		if err != nil {
			s.eventLogger().Error("session_cleanup_failed", map[string]any{"session_id": sessionID, "error": err})
		}
		RecordSessionKilled()
		s.eventLogger().Info("session_killed", map[string]any{"session_id": sessionID})
	}()

	RecordSessionStarted()
	s.eventLogger().Info("session_acquired", map[string]any{"session_id": sessionID})

	// 4. Send Response & Loop
	resp := &pb.AcquireResponse{
		SessionId:    session.ID,
		ProxyAddress: s.proxyAddress,
	}
	if err := stream.Send(resp); err != nil {
		s.eventLogger().Warn("acquire_response_send_failed", map[string]any{"session_id": sessionID, "error": err})
		return err
	}

	for {
		in, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			s.eventLogger().Warn("acquire_stream_broken", map[string]any{"session_id": sessionID, "error": err})
			return err
		}

		if in.GetType() == pb.RequestType_REQUEST_TYPE_PING {
			s.lifecycleManager.UpdateHeartbeat(sessionID)
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
