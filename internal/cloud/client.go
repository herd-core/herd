package cloud

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/herd-core/herd"
	"github.com/herd-core/herd/internal/config"
	"github.com/herd-core/herd/internal/lifecycle"
	herdv1 "github.com/herd-core/herd/internal/proto/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

// Client handles the bidirectional gRPC stream to the Elixir Control Plane.
type Client struct {
	cfg         config.CloudConfig
	nodeID      string
	interfaceIP string
	pool        *herd.Pool[*http.Client]
	lm          *lifecycle.Manager
	client      herdv1.HerdControlPlaneClient
	conn        *grpc.ClientConn
	stream      herdv1.HerdControlPlane_ConnectClient
}

func NewClient(cfg config.CloudConfig, interfaceIP string, pool *herd.Pool[*http.Client], lm *lifecycle.Manager) *Client {
	nodeID := cfg.NodeID
	if nodeID == "" {
		host, _ := os.Hostname()
		nodeID = host
	}
	return &Client{
		cfg:         cfg,
		nodeID:      nodeID,
		interfaceIP: interfaceIP,
		pool:        pool,
		lm:          lm,
	}
}

func (c *Client) Start(ctx context.Context) error {
	if !c.cfg.Enabled {
		return nil
	}

	log.Printf("Connecting to Cloud Control Plane at %s...", c.cfg.Endpoint)

	conn, err := c.dialWithFallback(ctx)
	if err != nil {
		return fmt.Errorf("failed to dial control plane: %w", err)
	}
	c.conn = conn
	c.client = herdv1.NewHerdControlPlaneClient(conn)

	stream, err := c.client.Connect(ctx)
	if err != nil {
		return fmt.Errorf("failed to open stream: %w", err)
	}
	c.stream = stream
	c.sendTelemetry() // Send initial telemetry immediately
	log.Printf("Cloud Control Plane connected! NodeID: %s", c.nodeID)

	// Start telemetry heartbeat
	go c.telemetryLoop(ctx)

	// Start command listener
	go c.commandLoop(ctx)

	return nil
}

func (c *Client) telemetryLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// Send initial heartbeat immediately
	c.sendTelemetry()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.sendTelemetry(); err != nil {
				log.Printf("failed to send telemetry: %v", err)
				return
			}
		}
	}
}

func (c *Client) sendTelemetry() error {
	stats := c.pool.Stats()

	sessions, _ := c.pool.ListSessions(context.Background())
	activeSessions := make([]*herdv1.SessionInfo, 0, len(sessions))

	// For each session, find the first host port mapping.
	// In a more complex setup, we might report all ports or a specific service port.
	for sid, w := range sessions {
		if fw, ok := w.(interface{ PortMappings() []herd.PortMapping }); ok {
			pms := fw.PortMappings()
			if len(pms) > 0 {
				activeSessions = append(activeSessions, &herdv1.SessionInfo{
					SessionId: sid,
					Port:      int32(pms[0].HostPort),
				})
			}
		}
	}

	return c.stream.Send(&herdv1.NodeStream{
		NodeId:            c.nodeID,
		AvailableMemoryMb: stats.Node.AvailableMemoryBytes / 1024 / 1024,
		ActiveVmCount:     int32(stats.ActiveSessions),
		CpuUsagePercent:   (1.0 - stats.Node.CPUIdle) * 100.0,
		UptimeSeconds:     time.Now().Unix(), // Ideally node uptime, but this works for now
		InterfaceIp:       c.interfaceIP,
		ActiveSessions:    activeSessions,
	})
}

func (c *Client) commandLoop(ctx context.Context) {
	for {
		cmd, err := c.stream.Recv()
		if err == io.EOF {
			log.Println("Cloud Control Plane stream closed by server")
			return
		}
		if err != nil {
			log.Printf("failed to receive command: %v", err)
			return
		}

		log.Printf("Received Cloud Command: %s (ID: %s)", cmd.Action, cmd.CommandId)

		if cmd.Action == "boot_vm" {
			image := cmd.Params["image"]
			if image == "" {
				image = "alpine:latest" // Default for testing
			}
			sessionID := fmt.Sprintf("session-%s", cmd.CommandId)

			log.Printf("Booting VM for session %s using image %s", sessionID, image)

			go func() {
				config := herd.TenantConfig{
					Image: image,
					// Add port mappings parsing if needed
					PortMappings: []herd.PortMapping{
						{HostPort: 0, GuestPort: 80, Protocol: "tcp"},
					},
				}
				_, err := c.pool.Acquire(ctx, sessionID, config)
				if err != nil {
					log.Printf("failed to boot VM for session %s: %v", sessionID, err)
					return
				}
				log.Printf("VM booted for session %s", sessionID)
			}()
		}

		if cmd.Action == "destroy_all" {
			log.Printf("Received destroy_all command, stopping all VMs...")
			if err := c.lm.StopAll(ctx); err != nil {
				log.Printf("failed to stop all VMs: %v", err)
			}
		}
	}
}

func (c *Client) Close() {
	if c.conn != nil {
		c.conn.Close()
	}
}

func (c *Client) dialWithFallback(ctx context.Context) (*grpc.ClientConn, error) {
	endpointHost, _, err := net.SplitHostPort(c.cfg.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid cloud endpoint %q, expected host:port: %w", c.cfg.Endpoint, err)
	}

	attemptTimeout := 8 * time.Second
	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: endpointHost,
	}

	attempts := []struct {
		name string
		cred credentials.TransportCredentials
	}{
		{name: "tls", cred: credentials.NewTLS(tlsCfg)},
		{name: "insecure", cred: insecure.NewCredentials()},
	}
	ka := keepalive.ClientParameters{
		Time:                60 * time.Second,
		Timeout:             10 * time.Second,
		PermitWithoutStream: true,
	}

	var lastErr error
	for _, a := range attempts {
		attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
		conn, err := grpc.DialContext(
			attemptCtx,
			c.cfg.Endpoint,
			grpc.WithTransportCredentials(a.cred),
			grpc.WithKeepaliveParams(ka),
			grpc.WithBlock(),
		)
		cancel()

		if err == nil {
			log.Printf("Cloud Control Plane dial succeeded using %s transport", a.name)
			return conn, nil
		}

		lastErr = err
		log.Printf("Cloud Control Plane dial with %s transport failed: %v", a.name, err)
	}

	return nil, fmt.Errorf("all dial attempts failed (last error: %w)", lastErr)
}
