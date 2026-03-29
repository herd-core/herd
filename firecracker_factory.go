package herd

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/herd-core/herd/internal/network"
	"github.com/herd-core/herd/internal/storage"
	"github.com/herd-core/herd/internal/vsock"
)

// FirecrackerFactory is a minimal implementation of a WorkerFactory that spawns Firecracker VMs.
type FirecrackerFactory struct {
	FirecrackerPath string
	KernelImagePath string
	// GuestAgentPath is the host path to the static herd-guest-agent binary; it is
	// copied into each VM rootfs before boot (no initrd).
	GuestAgentPath string
	Storage        *storage.Manager

	SocketPathDir string
	Command       []string
	IPAM          *network.IPAM
}

// FirecrackerWorker represents a single running Firecracker VM.
type FirecrackerWorker struct {
	storage    *storage.Manager
	id         string
	socketPath string
	tapName    string
	guestIP    string
	cmd        *exec.Cmd
	client     *http.Client
	ipam       *network.IPAM
}

// ID returns the worker ID.
func (f *FirecrackerWorker) ID() string {
	return f.id
}

// Address returns the socket path. Wait, returning empty string since vsock
// or unix domains can't be represented easily here.
func (f *FirecrackerWorker) Address() string {
	return fmt.Sprintf("unix://%s", f.socketPath)
}

// Client returns the HTTP client.
func (f *FirecrackerWorker) Client() *http.Client {
	return f.client
}

// Healthy checks if the VM is up. For now we just check if process is alive.
func (f *FirecrackerWorker) Healthy(ctx context.Context) error {
	// 1. Check if the Firecracker process has crashed (requires cmd.Wait() to be called in a goroutine)
	if f.cmd.ProcessState != nil && f.cmd.ProcessState.Exited() {
		return fmt.Errorf("firecracker process exited with code: %v", f.cmd.ProcessState.ExitCode())
	}

	// The HTTP ping check is temporarily disabled because the guest agent currently only speaks
	// raw JSON payload format on port 5000 and has no HTTP proxy multiplexer over vsock.
	return nil
}

// OnCrash sets a crash handler.
func (f *FirecrackerWorker) OnCrash(fn func(sessionID string)) {
	// Not implemented for this minimal version
}

// Close kills the VM and cleans up resources.
func (f *FirecrackerWorker) Close() error {
	if f.cmd != nil && f.cmd.Process != nil {
		_ = f.cmd.Process.Kill()
	}
	os.Remove(f.socketPath)

	_ = network.DeleteTap(f.tapName)
	if f.guestIP != "" && f.ipam != nil {
		f.ipam.Release(f.guestIP)
	}

	if f.storage != nil {
		f.storage.Teardown(context.Background(), f.id)
	}
	return nil
}

// Spawn starts a new Firecracker VM.
func (f *FirecrackerFactory) Spawn(ctx context.Context) (Worker[*http.Client], error) {
	spawnStart := time.Now()
	workerID := fmt.Sprintf("fc-%d", time.Now().UnixNano())
	socketPath := filepath.Join(f.SocketPathDir, fmt.Sprintf("%s.sock", workerID))

	t0 := time.Now()
	rootfsPath, err := f.Storage.Snapshot(ctx, workerID)
	if err != nil {
		return nil, fmt.Errorf("failed to create rootfs snapshot: %w", err)
	}
	log.Printf("[spawn:%s] Snapshot         %v", workerID, time.Since(t0))

	tInject := time.Now()
	if err := f.Storage.InjectGuestAgent(ctx, workerID, f.GuestAgentPath, storage.DefaultGuestAgentPath); err != nil {
		_ = f.Storage.Teardown(ctx, workerID)
		return nil, fmt.Errorf("inject guest agent: %w", err)
	}
	log.Printf("[spawn:%s] inject guest     %v", workerID, time.Since(tInject))

	// Ensure old socket is removed
	os.Remove(socketPath)

	t1 := time.Now()
	guestIP, err := f.IPAM.Acquire()
	if err != nil {
		return nil, fmt.Errorf("failed to acquire IP: %w", err)
	}
	log.Printf("[spawn:%s] IPAM.Acquire     %v", workerID, time.Since(t1))

	hostIP := "10.200.0.1"

	t2 := time.Now()
	tapName := "tap-" + workerID[len(workerID)-8:]
	_ = network.DeleteTap(tapName)
	if err := network.CreatePointToPointTap(tapName, hostIP, guestIP); err != nil {
		f.IPAM.Release(guestIP)
		return nil, fmt.Errorf("failed to create tap device: %w", err)
	}
	log.Printf("[spawn:%s] TAP setup        %v", workerID, time.Since(t2))

	// Create a minimal config json
	configPath := filepath.Join(f.SocketPathDir, fmt.Sprintf("%s.json", workerID))
	initPath := storage.DefaultGuestAgentPath
	configData := fmt.Sprintf(`{
		"boot-source": {
			"kernel_image_path": "%s",
			"boot_args": "console=ttyS0 reboot=k panic=1 pci=off quiet mitigations=off i8042.nokbd i8042.noaux tsc=reliable random.trust_cpu=on root=/dev/vda rw init=%s herd_rootfs=1 ip=%s gw=%s"
		},
		"drives": [
			{
				"drive_id": "rootfs",
				"path_on_host": "%s",
				"is_root_device": true,
				"is_read_only": false
			}
		],
		"network-interfaces": [
			{
				"iface_id": "eth0",
				"guest_mac": "AA:FC:00:00:00:0%s",
				"host_dev_name": "%s"
			}
		],
		"machine-config": {
			"vcpu_count": 1,
			"mem_size_mib": 128
		},
		"vsock": {
			"guest_cid": 3,
			"uds_path": "%s"
		}
	}`, f.KernelImagePath, initPath, guestIP, hostIP, rootfsPath, workerID[len(workerID)-1:], tapName, socketPath)

	if err := os.WriteFile(configPath, []byte(configData), 0644); err != nil {
		return nil, fmt.Errorf("failed to write config: %w", err)
	}

	// `--no-api` tells Firecracker to autoboot the machine using the provided config immediately
	cmd := exec.CommandContext(ctx, f.FirecrackerPath, "--no-api", "--config-file", configPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	t3 := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start firecracker: %w", err)
	}
	log.Printf("[spawn:%s] cmd.Start        %v", workerID, time.Since(t3))

	// Wait for the process in the background so cmd.ProcessState is populated on crash
	go func() {
		_ = cmd.Wait()
	}()

	// Loop to wait for the VM to boot and accept vsock connections.
	// 30s accommodates concurrent boot contention when multiple VMs
	// share host CPU during pool initialization.
	t4 := time.Now()
	deadline := time.Now().Add(30 * time.Second)
	var execConn net.Conn
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := vsock.DialFirecracker(ctx, socketPath, 5000)
		if err == nil {
			execConn = conn
			break
		}
		lastErr = err
		time.Sleep(10 * time.Millisecond)
	}
	log.Printf("[spawn:%s] vsock connect    %v", workerID, time.Since(t4))

	if execConn == nil {
		return nil, fmt.Errorf("failed to connect to guest agent vsock port 5000 within timeout: %v", lastErr)
	}

	// Stream the workload payload down the vsock pipe
	go func() {
		payload := vsock.ExecPayload{Command: f.Command}
		if err := vsock.Execute(context.Background(), execConn, payload, os.Stdout); err != nil {
			fmt.Printf("[host] Failed to execute payload on %s: %v\n", workerID, err)
		}
	}()

	// Create a custom HTTP Client that dials over vsock for HTTP routing
	agentClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				// We ignore the actual address and force route to Guest CID 3 over the Firecracker UDS
				return vsock.DialFirecracker(ctx, socketPath, 5000)
			},
		},
	}

	log.Printf("[spawn:%s] TOTAL            %v", workerID, time.Since(spawnStart))

	return &FirecrackerWorker{
		id:         workerID,
		socketPath: socketPath,
		tapName:    tapName,
		guestIP:    guestIP,
		cmd:        cmd,
		client:     agentClient,
		storage:    f.Storage,
		ipam:       f.IPAM,
	}, nil
}
