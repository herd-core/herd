package daemon

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/herd-core/herd"
	pb "github.com/herd-core/herd/proto/herd/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

const DrainingTimeout = 30 * time.Second

// SessionState represents the lifecycle state of a session.
// Active -> Draining -> (removed from map)
type SessionState int

const (
	SessionActive   SessionState = iota
	SessionDraining              // client disconnected, grace period running
)

// SessionInfo holds the live state for a single session.
type SessionInfo struct {
	session *herd.Session[*http.Client]
	state   SessionState
	cancel  context.CancelFunc // cancels the drain timer on reconnect
	mu      sync.Mutex
}

// Server implements the HerdService gRPC server.
type Server struct {
	pb.UnimplementedHerdServiceServer
	pool         *herd.Pool[*http.Client]
	proxyAddress string
	maxWorkers   int
	seq          atomic.Uint64
	logger       *EventLogger
	sessions     sync.Map // map[string]*SessionInfo (session id -> session info)
}

func NewServer(pool *herd.Pool[*http.Client], proxyAddress string, maxWorkers int, logger *EventLogger) *Server {
	return &Server{pool: pool, proxyAddress: proxyAddress, maxWorkers: maxWorkers, logger: logger}
}

// tryReconnect looks up a draining session by ID and transitions it back to Active.
// Returns the SessionInfo on success, or a gRPC status error if the session is
// unknown, already active, or in an unexpected state.
func (s *Server) tryReconnect(sessionID string) (*SessionInfo, error) {
	v, ok := s.sessions.Load(sessionID)
	if !ok {
		// Session was stopped (removed from map) or never existed.
		return nil, status.Errorf(codes.NotFound, "session %q not found or has expired", sessionID)
	}
	info := v.(*SessionInfo)
	info.mu.Lock()
	defer info.mu.Unlock()

	switch info.state {
	case SessionActive:
		return nil, status.Errorf(codes.FailedPrecondition, "session %q is already active", sessionID)
	case SessionDraining:
		info.cancel()            // cancel the drain timer
		info.state = SessionActive
		RecordSessionResumed()
		return info, nil
	default:
		return nil, status.Errorf(codes.Internal, "session %q is in unknown state", sessionID)
	}
}

// markDraining transitions a session to Draining and starts the grace-period timer.
// If the client does not reconnect within DrainingTimeout, the session is killed
// and removed from the map. Called when the stream breaks unexpectedly.
func (s *Server) markDraining(info *SessionInfo) {
	if info == nil {
		s.eventLogger().Error("mark_draining_nil_info", map[string]any{})
		return
	}
	info.mu.Lock()
	defer info.mu.Unlock()

	if info.state != SessionActive {
		return
	}
	info.state = SessionDraining
	sessionID := info.session.ID
	RecordSessionDraining()

	ctx, cancel := context.WithTimeout(context.Background(), DrainingTimeout)
	info.cancel = cancel

	go func() {
		defer cancel()
		<-ctx.Done()

		info.mu.Lock()
		defer info.mu.Unlock()
		// Only kill if still draining — reconnect would have flipped state back to Active.
		if info.state != SessionDraining {
			return
		}
		s.sessions.Delete(sessionID)
		if err := s.pool.KillSession(sessionID); err != nil {
			s.eventLogger().Error("session_kill_failed", map[string]any{"session_id": sessionID, "error": err})
			return
		}
		RecordSessionKilled()
		s.eventLogger().Info("session_killed", map[string]any{"session_id": sessionID})
	}()
}

// killSessionNow immediately kills a session and removes it from the map.
// Used on clean EOF — no grace period needed.
func (s *Server) killSessionNow(info *SessionInfo) {
	if info == nil {
		return
	}
	sessionID := info.session.ID
	s.sessions.Delete(sessionID)
	if err := s.pool.KillSession(sessionID); err != nil {
		s.eventLogger().Error("session_kill_failed", map[string]any{"session_id": sessionID, "error": err})
		return
	}
	RecordSessionKilled()
	s.eventLogger().Info("session_killed", map[string]any{"session_id": sessionID})
}

// Acquire handles bidirectional streaming allocation.
//
// Two entry flows:
//  1. No session ID in request  → allocate a new session.
//  2. Session ID present        → reconnect to a Draining session.
//
// On clean client close (EOF)   → kill immediately.
// On broken stream              → mark Draining; client has DrainingTimeout to reconnect.
func (s *Server) Acquire(stream pb.HerdService_AcquireServer) error {
	if s.pool == nil {
		return status.Error(codes.FailedPrecondition, "daemon server is not configured with a pool")
	}

	// Read the first message to determine which flow we're in.
	req, err := stream.Recv()
	if err == io.EOF {
		return nil
	}
	if err != nil {
		return err
	}
	RecordAcquireRequest()

	var info *SessionInfo

	if reconnectID := req.GetSessionId(); reconnectID != "" {
		// ── Flow 2: reconnect to a draining session ──────────────────────────
		s.eventLogger().Info("acquire_reconnect_attempt", map[string]any{"session_id": reconnectID})
		info, err = s.tryReconnect(reconnectID)
		if err != nil {
			return err
		}
		s.eventLogger().Info("session_reconnected", map[string]any{"session_id": reconnectID})
	} else {
		// ── Flow 1: allocate a new session ───────────────────────────────────
		sessionID := fmt.Sprintf("sess-%d", s.seq.Add(1))
		s.eventLogger().Info("acquire_request_received", map[string]any{"worker_type": req.GetWorkerType(), "session_id": sessionID})

		acquireCtx := stream.Context()
		if req.GetTimeoutSeconds() > 0 {
			var cancelAcquire context.CancelFunc
			acquireCtx, cancelAcquire = context.WithTimeout(stream.Context(), time.Duration(req.GetTimeoutSeconds())*time.Second)
			defer cancelAcquire()
		}

		session, acquireErr := s.pool.Acquire(acquireCtx, sessionID)
		if acquireErr != nil {
			RecordAcquireFailure()
			s.eventLogger().Error("session_acquire_failed", map[string]any{"session_id": sessionID, "error": acquireErr})
			return status.Errorf(codes.ResourceExhausted, "acquire session failed: %v", acquireErr)
		}

		info = &SessionInfo{session: session, state: SessionActive}
		s.sessions.Store(sessionID, info)
		RecordSessionStarted()
		s.eventLogger().Info("session_acquired", map[string]any{"session_id": sessionID})
	}

	// ── Stream loop ───────────────────────────────────────────────────────────
	// Send the first response, then keep the stream alive receiving heartbeats.
	for {
		resp := &pb.AcquireResponse{
			SessionId:    info.session.ID,
			ProxyAddress: s.proxyAddress,
			WorkerPid:    0,
		}
		if err := stream.Send(resp); err != nil {
			s.eventLogger().Warn("acquire_response_send_failed", map[string]any{"session_id": info.session.ID, "error": err})
			s.markDraining(info)
			return err
		}

		// keeping stream alive for heartbeats
		_, err = stream.Recv()
		if err == io.EOF {
			s.eventLogger().Info("acquire_stream_closed", map[string]any{"session_id": info.session.ID})
			s.killSessionNow(info)
			return nil
		}
		if err != nil {
			s.eventLogger().Warn("acquire_stream_broken", map[string]any{"session_id": info.session.ID, "error": err})
			s.markDraining(info)
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
