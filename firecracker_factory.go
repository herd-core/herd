package herd

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
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
	done       chan struct{} // closed when cmd.Wait() returns
}

// ID returns the worker ID.
func (f *FirecrackerWorker) ID() string {
	return f.id
}

// GuestIP returns the internal IP allocated to the worker.
func (f *FirecrackerWorker) GuestIP() string {
	return f.guestIP
}

// Address returns the HTTP base URL for the workload on the guest LAN (data-plane
// reverse proxy). Exec and other vsock paths use VsockUDSPath.
func (f *FirecrackerWorker) Address() string {
	return fmt.Sprintf("http://%s:80", f.guestIP)
}

// VsockUDSPath is the host Unix socket Firecracker exposes for vsock connect.
func (f *FirecrackerWorker) VsockUDSPath() string {
	return f.socketPath
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

// Close kills the VM and cleans up resources. It blocks until the Firecracker
// process has fully exited before tearing down storage, preventing "device busy"
// errors on the devmapper thin volume.
func (f *FirecrackerWorker) Close() error {
	if f.cmd != nil && f.cmd.Process != nil {
		_ = f.cmd.Process.Kill()
		select {
		case <-f.done:
		case <-time.After(5 * time.Second):
			slog.Warn("timed out waiting for firecracker process to exit", "vmID", f.id)
		}
	}

	if err := os.Remove(f.socketPath); err != nil {
		slog.Warn("failed to remove firecracker socket", "path", f.socketPath, "error", err)
	}

	_ = network.DeleteTap(f.tapName)
	if f.guestIP != "" && f.ipam != nil {
		f.ipam.Release(f.guestIP)
	}

	if f.storage != nil {
		if err := f.storage.Teardown(context.Background(), f.id); err != nil {
			slog.Error("failed to teardown storage", "vmID", f.id, "error", err)
		}
	}
	return nil
}

// WarmImage ensures an image is cached without starting a VM.
func (f *FirecrackerFactory) WarmImage(ctx context.Context, imageRef string) error {
	return f.Storage.WarmImage(ctx, imageRef)
}

// Spawn starts a new Firecracker VM.
func (f *FirecrackerFactory) Spawn(ctx context.Context, sessionID string, config TenantConfig) (Worker[*http.Client], error) {
	spawnStart := time.Now()
	workerID := sessionID // Use sessionID universally
	socketPath := filepath.Join(f.SocketPathDir, fmt.Sprintf("%s.sock", workerID))

	t0 := time.Now()
	// Ensure the image is warmed (pulled) before we try to extract config or snapshot
	if err := f.Storage.WarmImage(ctx, config.Image); err != nil {
		return nil, fmt.Errorf("failed to warm image: %w", err)
	}

	imgConfig, err := f.Storage.ExtractImageConfig(ctx, config.Image)
	if err != nil {
		return nil, fmt.Errorf("failed to extract image config: %w", err)
	}

	finalCmd := imgConfig.Cmd
	if len(config.Command) > 0 {
		finalCmd = config.Command
	}
	if len(imgConfig.Entrypoint) > 0 {
		if len(config.Command) == 0 {
			finalCmd = append(imgConfig.Entrypoint, finalCmd...)
		} else {
			finalCmd = append(imgConfig.Entrypoint, config.Command...)
		}
	}
	if len(finalCmd) == 0 {
		return nil, fmt.Errorf("no command to run: image %q has no Entrypoint or Cmd and none was provided in the request", config.Image)
	}

	rootfsPath, err := f.Storage.Snapshot(ctx, workerID, config.Image)
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
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		slog.Warn("failed to remove legacy socket", "path", socketPath, "error", err)
	}

	t1 := time.Now()
	guestIP, err := f.IPAM.Acquire()
	if err != nil {
		return nil, fmt.Errorf("failed to acquire IP: %w", err)
	}
	log.Printf("[spawn:%s] IPAM.Acquire     %v", workerID, time.Since(t1))

	hostIP := "10.200.0.1"

	t2 := time.Now()
	tapNameLen := len(workerID)
	if tapNameLen > 8 {
		tapNameLen = 8
	}
	tapName := "tap-" + workerID[len(workerID)-tapNameLen:]
	_ = network.DeleteTap(tapName)
	if err := network.CreatePointToPointTap(tapName, hostIP, guestIP); err != nil {
		f.IPAM.Release(guestIP)
		return nil, fmt.Errorf("failed to create tap device: %w", err)
	}
	log.Printf("[spawn:%s] TAP setup        %v", workerID, time.Since(t2))

	macByte := fmt.Sprintf("%02x", time.Now().UnixNano()%256)

	// Create a minimal config json
	configPath := filepath.Join(f.SocketPathDir, fmt.Sprintf("%s.json", workerID))
	initPath := storage.DefaultGuestAgentPath
	configData := fmt.Sprintf(`{
		"boot-source": {
			"kernel_image_path": "%s",
			"boot_args": "console=ttyS0 reboot=k panic=1 pci=off quiet mitigations=off i8042.nokbd i8042.noaux tsc=reliable random.trust_cpu=on random.trust_bootloader=on root=/dev/vda rw rootfstype=ext4 rootflags=noload,noinit_itable init=%s herd_rootfs=1 herd_ip=%s herd_gw=%s"
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
				"guest_mac": "AA:FC:00:00:00:%s",
				"host_dev_name": "%s"
			}
		],
		"machine-config": {
			"vcpu_count": 1,
			"mem_size_mib": 512
		},
		"vsock": {
			"guest_cid": 3,
			"uds_path": "%s"
		},
		"entropy": {}
	}`, f.KernelImagePath, initPath, guestIP, hostIP, rootfsPath, macByte, tapName, socketPath)

	if err := os.WriteFile(configPath, []byte(configData), 0644); err != nil {
		return nil, fmt.Errorf("failed to write config: %w", err)
	}

	// The Firecracker process must outlive the HTTP request that spawned it, so use
	// context.Background() instead of the request-scoped ctx. The request ctx is still
	// used above for the spawn-time operations (image pull, snapshot, vsock wait).
	cmd := exec.Command(f.FirecrackerPath, "--no-api", "--config-file", configPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	t3 := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start firecracker: %w", err)
	}
	log.Printf("[spawn:%s] cmd.Start        %v", workerID, time.Since(t3))

	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
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

	logPath := filepath.Join(f.SocketPathDir, fmt.Sprintf("%s.log", workerID))
	logFile, _ := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)

	finalEnv := imgConfig.Env
	for k, v := range config.Env {
		finalEnv = append(finalEnv, fmt.Sprintf("%s=%s", k, v))
	}

	// Stream the workload payload down the vsock pipe
	go func() {
		defer func() {
			if cerr := logFile.Close(); cerr != nil {
				slog.Warn("failed to close worker log file", "error", cerr)
			}
		}()
		payload := vsock.ExecPayload{
			Command: finalCmd,
			Env:     finalEnv,
		}
		if err := vsock.Execute(context.Background(), execConn, payload, io.MultiWriter(os.Stdout, logFile)); err != nil {
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
		done:       done,
	}, nil
}
