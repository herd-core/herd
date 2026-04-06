package cloud

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/herd-core/herd"
	"github.com/herd-core/herd/internal/config"
	"github.com/herd-core/herd/internal/daemon"
	herdv1 "github.com/herd-core/herd/internal/proto/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
)

// Client handles the bidirectional gRPC stream to the Elixir Control Plane.
type Client struct {
	cfg         config.CloudConfig
	interfaceIP string
	controller  *daemon.Controller
	client      herdv1.HerdControlPlaneClient
	conn        *grpc.ClientConn
	stream      herdv1.HerdControlPlane_ConnectClient
}

func NewClient(cfg config.CloudConfig, interfaceIP string, controller *daemon.Controller) *Client {
	return &Client{
		cfg:         cfg,
		interfaceIP: interfaceIP,
		controller:  controller,
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
	c.loadLocalIdentity()

	var authCtx context.Context
	if c.cfg.NodeKey != "" && c.cfg.NodeID != "" {
		log.Printf("Authenticating with persistent NodeKey for ID: %s", c.cfg.NodeID)
		authCtx = metadata.AppendToOutgoingContext(ctx,
			"x-node-id", c.cfg.NodeID,
			"x-node-key", c.cfg.NodeKey,
		)
	} else if c.cfg.MachineToken != "" {
		log.Printf("Authenticating with one-time Machine Token")
		authCtx = metadata.AppendToOutgoingContext(ctx, "x-machine-token", c.cfg.MachineToken)
	} else {
		return fmt.Errorf("no authentication credentials available (machine token or node key)")
	}

	stream, err := c.client.Connect(authCtx)
	if err != nil {
		return fmt.Errorf("failed to open stream: %w", err)
	}
	c.stream = stream
	c.sendTelemetry(ctx) // Send initial telemetry immediately
	log.Printf("Cloud Control Plane connected!")

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
	c.sendTelemetry(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.sendTelemetry(ctx); err != nil {
				log.Printf("failed to send telemetry: %v", err)
				return
			}
		}
	}
}

func (c *Client) sendTelemetry(ctx context.Context) error {
	stats := c.controller.Pool().Stats()

	activeSessionsRaw := c.controller.ListSessions(ctx)
	activeSessions := make([]*herdv1.SessionInfo, 0, len(activeSessionsRaw))

	// For each session, find the first host port mapping.
	// In a more complex setup, we might report all ports or a specific service port.
	for _, s := range activeSessionsRaw {
		activeSessions = append(activeSessions, &herdv1.SessionInfo{
			SessionId: s.SessionID,
			// For now, telemetry from control plane dashboard doesn't need the port mapping
			// as it's primarily used for routing in NodeTwin. But we could add it back
			// if we query the worker for host port.
		})
	}

	return c.stream.Send(&herdv1.NodeStream{
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
			log.Printf("Booting VM for command %s", cmd.CommandId)

			go func() {
				req := daemon.SessionCreateRequest{
					Image: cmd.Params["image"],
					PortMappings: []herd.PortMapping{
						{HostPort: 0, GuestPort: 80, Protocol: "tcp"},
					},
				}
				if req.Image == "" {
					req.Image = "alpine:latest"
				}

				resp, err := c.controller.CreateSession(ctx, req)
				if err != nil {
					log.Printf("failed to boot VM for command %s: %v", cmd.CommandId, err)
					return
				}
				log.Printf("VM booted for session %s", resp.SessionID)
			}()
		}

		if cmd.Action == "destroy_all" {
			log.Printf("Received destroy_all command")
		}

		if cmd.Action == "swap_credentials" {
			nodeID := cmd.Params["node_id"]
			nodeKey := cmd.Params["node_key"]
			if nodeID == "" || nodeKey == "" {
				log.Printf("received malformed swap_credentials command")
				continue
			}

			log.Printf("Credential Swap triggered! Persisting new NodeKey for ID: %s", nodeID)
			if err := c.persistIdentity(nodeID, nodeKey); err != nil {
				log.Printf("failed to persist new identity: %v", err)
				continue
			}
			log.Printf("✅ NodeKey persisted to %s. One-time token approach is now superseded.", c.cfg.NodeKeyPath)
		}
	}
}

func (c *Client) loadLocalIdentity() {
	keyData, err := os.ReadFile(c.cfg.NodeKeyPath)
	if err == nil {
		c.cfg.NodeKey = strings.TrimSpace(string(keyData))
	}

	idPath := filepath.Join(filepath.Dir(c.cfg.NodeKeyPath), "node.id")
	idData, err := os.ReadFile(idPath)
	if err == nil {
		c.cfg.NodeID = strings.TrimSpace(string(idData))
	}
}

func (c *Client) persistIdentity(nodeID, nodeKey string) error {
	c.cfg.NodeID = nodeID
	c.cfg.NodeKey = nodeKey

	dir := filepath.Dir(c.cfg.NodeKeyPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	// P0 Fix: Remove existing read-only file before writing a new one
	os.Remove(c.cfg.NodeKeyPath)

	// Write key with restricted permissions
	if err := os.WriteFile(c.cfg.NodeKeyPath, []byte(nodeKey), 0600); err != nil {
		return err
	}

	idPath := filepath.Join(dir, "node.id")
	return os.WriteFile(idPath, []byte(nodeID), 0400)
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
