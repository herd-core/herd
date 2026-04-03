package daemon

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/herd-core/herd"
	"github.com/herd-core/herd/internal/lifecycle"
)

// SessionCreateRequest is the shared request structure for booting VMs.
type SessionCreateRequest struct {
	Image              string             `json:"image"`
	Command            []string           `json:"command,omitempty"`
	Env                map[string]string  `json:"env,omitempty"`
	IdleTimeoutSeconds int                `json:"idle_timeout_seconds,omitempty"`
	TTLSeconds         int                `json:"ttl_seconds,omitempty"`
	HealthInterval     string             `json:"health_interval,omitempty"`
	Warm               bool               `json:"warm,omitempty"`
	PortMappings       []herd.PortMapping `json:"port_mappings,omitempty"`
}

// SessionCreateResponse is the shared response structure after booting VMs.
type SessionCreateResponse struct {
	SessionID    string             `json:"session_id"`
	InternalIP   string             `json:"internal_ip"`
	ProxyAddress string             `json:"proxy_address"`
	PortMappings []herd.PortMapping `json:"port_mappings,omitempty"`
}

type Controller struct {
	pool             *herd.Pool[*http.Client]
	lifecycleManager *lifecycle.Manager
	logger           *EventLogger
	proxyAddress     string
	seq              atomic.Uint64
}

func NewController(
	pool *herd.Pool[*http.Client],
	lifecycleManager *lifecycle.Manager,
	proxyAddress string,
	logger *EventLogger,
) *Controller {
	return &Controller{
		pool:             pool,
		lifecycleManager: lifecycleManager,
		proxyAddress:     proxyAddress,
		logger:           logger,
	}
}

func (c *Controller) Pool() *herd.Pool[*http.Client] {
	return c.pool
}

func (c *Controller) CreateSession(ctx context.Context, req SessionCreateRequest) (*SessionCreateResponse, error) {
	RecordAcquireRequest()

	if req.Warm {
		if err := c.pool.Factory().WarmImage(ctx, req.Image); err != nil {
			c.logger.Error("failed_to_warm_image", map[string]any{"error": err, "image": req.Image})
			return nil, fmt.Errorf("failed to warm image: %w", err)
		}
	}

	sessionID := fmt.Sprintf("sess-%d-%d", time.Now().UnixNano(), c.seq.Add(1))
	c.logger.Info("acquire_request_received", map[string]any{"session_id": sessionID})

	tenantConfig := herd.TenantConfig{
		Image:              req.Image,
		Command:            req.Command,
		Env:                req.Env,
		IdleTimeoutSeconds: req.IdleTimeoutSeconds,
		TTLSeconds:         req.TTLSeconds,
		HealthInterval:     req.HealthInterval,
		PortMappings:       req.PortMappings,
	}

	session, err := c.pool.Acquire(ctx, sessionID, tenantConfig)
	if err != nil {
		RecordAcquireFailure()
		c.logger.Error("session_acquire_failed", map[string]any{"session_id": sessionID, "error": err})
		return nil, fmt.Errorf("failed to acquire session: %w", err)
	}

	c.lifecycleManager.Register(sessionID, tenantConfig)
	RecordSessionStarted()
	c.logger.Info("session_acquired", map[string]any{"session_id": sessionID})

	var internalIP string
	if fw, ok := session.Worker.(interface{ GuestIP() string }); ok {
		internalIP = fw.GuestIP()
	}

	resp := &SessionCreateResponse{
		SessionID:    sessionID,
		InternalIP:   internalIP,
		ProxyAddress: c.proxyAddress,
		PortMappings: req.PortMappings,
	}

	if fw, ok := session.Worker.(interface{ PortMappings() []herd.PortMapping }); ok {
		resp.PortMappings = fw.PortMappings()
	}

	return resp, nil
}

func (c *Controller) DeleteSession(ctx context.Context, sessionID string, reason string) error {
	err := c.lifecycleManager.UnregisterAndKill(sessionID, reason)
	if err != nil {
		c.logger.Error("session_cleanup_failed", map[string]any{"session_id": sessionID, "error": err})
		return err
	}

	RecordSessionKilled()
	c.logger.Info("session_killed", map[string]any{"session_id": sessionID})
	return nil
}

func (c *Controller) ListSessions(ctx context.Context) []*lifecycle.SessionState {
	return c.lifecycleManager.ListSessions()
}

func (c *Controller) UpdateHeartbeat(ctx context.Context, sessionID string) {
	c.lifecycleManager.UpdateHeartbeat(sessionID)
}

func (c *Controller) GetLogs(ctx context.Context, sessionID string) (io.ReadCloser, error) {
	sess, err := c.pool.GetSession(ctx, sessionID)
	if err != nil || sess == nil {
		return nil, fmt.Errorf("session not found")
	}

	var workerID string
	if fw, ok := sess.Worker.(interface{ ID() string }); ok {
		workerID = fw.ID()
	} else {
		return nil, fmt.Errorf("worker does not support logs")
	}

	logPath := fmt.Sprintf("/tmp/%s.log", workerID)
	f, err := os.Open(logPath)
	if err != nil {
		return nil, fmt.Errorf("logs not available: %w", err)
	}

	return f, nil
}

func (c *Controller) WarmImage(ctx context.Context, image string) error {
	if warmer, ok := c.pool.Factory().(interface{ WarmImage(context.Context, string) error }); ok {
		return warmer.WarmImage(ctx, image)
	}
	return fmt.Errorf("warming not supported by factory")
}
