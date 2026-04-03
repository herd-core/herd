package cloud

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"time"

	"github.com/herd-core/herd/internal/config"
	herdv1 "github.com/herd-core/herd/internal/proto/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

// Client handles the bidirectional gRPC stream to the Elixir Control Plane.
type Client struct {
	cfg    config.CloudConfig
	nodeID string
	client herdv1.HerdControlPlaneClient
	conn   *grpc.ClientConn
	stream herdv1.HerdControlPlane_ConnectClient
}

func NewClient(cfg config.CloudConfig) *Client {
	nodeID := cfg.NodeID
	if nodeID == "" {
		host, _ := os.Hostname()
		nodeID = host
	}
	return &Client{
		cfg:    cfg,
		nodeID: nodeID,
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
	// Simple dummy telemetry for now
	return c.stream.Send(&herdv1.NodeStream{
		NodeId:            c.nodeID,
		AvailableMemoryMb: 8192, // Mock data
		ActiveVmCount:     0,
		CpuUsagePercent:   5.0,
		UptimeSeconds:     time.Now().Unix(),
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
		
		// TODO (Implementation Detail):
		// When cmd.Action == "boot_vm", parse 'params' for 'image' and 'publish' port mappings.
		// Example: publish=eth0:8080:80:tcp,eth1:30042:80:tcp
		// Map these to herd.TenantConfig and call c.pool.Acquire(ctx, sessionID, config).
		// This requires passing the herd.Pool into NewClient() in cmd/herd/start.go.
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
