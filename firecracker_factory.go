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
	"strconv"
	"syscall"
	"time"

	"github.com/herd-core/herd/internal/network"
	"github.com/herd-core/herd/internal/storage"
	"github.com/herd-core/herd/internal/uid"
	"github.com/herd-core/herd/internal/vsock"
)

// FirecrackerFactory is a WorkerFactory that spawns Firecracker VMs via the
// jailer binary for secure, unprivileged isolation.
type FirecrackerFactory struct {
	// FirecrackerPath is the absolute host path to the firecracker binary.
	// The jailer exec's it inside the chroot; it never runs as root.
	FirecrackerPath string
	// JailerPath is the absolute host path to the jailer binary.
	JailerPath string
	// KernelImagePath is the shared host path to the guest kernel image.
	// It is hard-linked into each VM's chroot at /run/vmlinux before boot.
	KernelImagePath string
	// GuestAgentPath is the host path to the static herd-guest-agent binary.
	GuestAgentPath string

	Storage *storage.Manager
	IPAM    *network.IPAM
	PortManager *network.PortManager

	// UIDPool is the per-VM UID/GID allocator. Each Spawn leases a unique UID
	// from the pool; Close returns it. This guarantees every concurrent microVM
	// runs in a distinct DAC security domain, preventing lateral movement
	// between tenants on the same host.
	UIDPool *uid.Pool
	// JailerChrootBaseDir is the root under which the jailer creates per-VM
	// chroot directories: <JailerChrootBaseDir>/firecracker/<vmID>/root/
	JailerChrootBaseDir string
}

// chrootRoot returns the chroot root directory for a given VM.
// This matches the path the jailer creates:
//
//	<JailerChrootBaseDir>/firecracker/<vmID>/root
func (f *FirecrackerFactory) chrootRoot(vmID string) string {
	return filepath.Join(f.JailerChrootBaseDir, "firecracker", vmID, "root")
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
	chrootDir  string             // full chroot root path; removed on Close()
	done       chan struct{}       // closed when cmd.Wait() returns
	ctx        context.Context    // lifecycle context for the worker
	cancel     context.CancelFunc // cancelled on Close()
	leasedUID    int                // UID leased from UIDPool for this VM
	uidPool      *uid.Pool          // pool to return the UID to on Close()
	portMappings []PortMapping      // active port forwards on the host
	portManager  *network.PortManager // manager to release ports to on Close()
}

// ID returns the worker ID.
func (f *FirecrackerWorker) ID() string {
	return f.id
}

// GuestIP returns the internal IP allocated to the worker.
func (f *FirecrackerWorker) GuestIP() string {
	return f.guestIP
}

// Address returns the HTTP base URL for the workload on the guest LAN.
func (f *FirecrackerWorker) Address() string {
	return fmt.Sprintf("http://%s:80", f.guestIP)
}

// VsockUDSPath is the host-visible Unix socket Firecracker exposes for vsock.
func (f *FirecrackerWorker) VsockUDSPath() string {
	return f.socketPath
}

// PortMappings returns the active port forwards for this worker.
func (f *FirecrackerWorker) PortMappings() []PortMapping {
	return f.portMappings
}

// Client returns the HTTP client.
func (f *FirecrackerWorker) Client() *http.Client {
	return f.client
}

// Healthy checks if the VM is up by verifying the process hasn't exited.
func (f *FirecrackerWorker) Healthy(ctx context.Context) error {
	if f.cmd.ProcessState != nil && f.cmd.ProcessState.Exited() {
		return fmt.Errorf("firecracker process exited with code: %v", f.cmd.ProcessState.ExitCode())
	}
	return nil
}

// OnCrash sets a crash handler.
func (f *FirecrackerWorker) OnCrash(fn func(sessionID string)) {
	// Not implemented for this minimal version
}

// Close kills the VM and cleans up all resources. It blocks until the
// Firecracker process has fully exited before tearing down storage and the
// chroot, preventing "device busy" errors. The leased UID is returned to the
// pool after process exit so it cannot be reused while the old process is
// still alive.
func (f *FirecrackerWorker) Close() error {
	if f.cancel != nil {
		f.cancel()
	}
	if f.cmd != nil && f.cmd.Process != nil {
		_ = f.cmd.Process.Kill()
		select {
		case <-f.done:
		case <-time.After(5 * time.Second):
			slog.Warn("timed out waiting for firecracker process to exit", "vmID", f.id)
		}
	}

	// Remove the entire chroot jail directory tree (includes the vsock socket,
	// the config JSON, the kernel hard-link, and the rootfs device node).
	if f.chrootDir != "" {
		if err := os.RemoveAll(f.chrootDir); err != nil {
			slog.Warn("failed to remove jailer chroot directory", "path", f.chrootDir, "error", err)
		}
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

	// Return the leased UID to the pool last, after the process has fully
	// exited and all filesystem resources have been removed. This prevents a
	// race where a new VM is assigned the same UID before the old chroot is
	// fully gone.
	if f.uidPool != nil && f.leasedUID != 0 {
		if err := f.uidPool.Return(f.leasedUID); err != nil {
			slog.Error("failed to return uid to pool", "vmID", f.id, "uid", f.leasedUID, "error", err)
		}
	}

	for _, pm := range f.portMappings {
		_ = network.RemovePortMapping(pm.HostInterface, pm.HostPort, f.guestIP, pm.GuestPort, pm.Protocol)
		if f.portManager != nil {
			f.portManager.Release(pm.HostPort)
		}
	}

	return nil
}

// WarmImage ensures an image is cached without starting a VM.
func (f *FirecrackerFactory) WarmImage(ctx context.Context, imageRef string) error {
	return f.Storage.WarmImage(ctx, imageRef)
}

// Spawn starts a new Firecracker VM under the jailer with a unique leased UID.
func (f *FirecrackerFactory) Spawn(ctx context.Context, sessionID string, config TenantConfig) (worker Worker[*http.Client], err error) {
	spawnStart := time.Now()
	workerID := sessionID

	// 1. Initialize the Undo Stack
	var undoStack []func()

	// 2. Register the Defer Executor (LIFO)
	defer func() {
		if err != nil {
			// We failed somewhere. Execute the undo stack in Reverse Order (LIFO)
			slog.Warn("Spawn failed, rolling back resources...", "vmID", workerID, "error", err)
			for i := len(undoStack) - 1; i >= 0; i-- {
				undoStack[i]()
			}
		}
	}()

	// Lease a unique UID/GID for this VM.
	leasedUID, err := f.UIDPool.Checkout()
	if err != nil {
		return nil, fmt.Errorf("uid pool checkout: %w", err)
	}
	undoStack = append(undoStack, func() {
		_ = f.UIDPool.Return(leasedUID)
	})

	t0 := time.Now()
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
	undoStack = append(undoStack, func() {
		_ = f.Storage.Teardown(ctx, workerID)
	})
	log.Printf("[spawn:%s] Snapshot         %v", workerID, time.Since(t0))

	tInject := time.Now()
	if err := f.Storage.InjectGuestAgent(ctx, workerID, f.GuestAgentPath, storage.DefaultGuestAgentPath); err != nil {
		return nil, fmt.Errorf("inject guest agent: %w", err)
	}
	log.Printf("[spawn:%s] inject guest     %v", workerID, time.Since(tInject))

	// -------------------------------------------------------------------------
	// Build the per-VM chroot.
	// -------------------------------------------------------------------------
	chrootRoot := f.chrootRoot(workerID)
	chrootRunDir := filepath.Join(chrootRoot, "run")
	chrootDevDir := filepath.Join(chrootRoot, "dev")

	// Ensure the parent directory <base>/firecracker/<id> exists.
	if err := os.MkdirAll(filepath.Dir(chrootRoot), 0755); err != nil {
		return nil, fmt.Errorf("create jail base dir: %w", err)
	}

	// Create the chroot root with 0700.
	if err := os.Mkdir(chrootRoot, 0700); err != nil && !os.IsExist(err) {
		return nil, fmt.Errorf("create chroot root: %w", err)
	}
	undoStack = append(undoStack, func() {
		_ = os.RemoveAll(chrootRoot)
	})

	// Chown the chroot root to the leased UID/GID.
	if err := os.Chown(chrootRoot, leasedUID, leasedUID); err != nil {
		return nil, fmt.Errorf("chown chroot root: %w", err)
	}

	// Create run/dev dirs with 0700.
	if err := os.Mkdir(chrootRunDir, 0700); err != nil && !os.IsExist(err) {
		return nil, fmt.Errorf("create chroot run dir: %w", err)
	}
	if err := os.Chown(chrootRunDir, leasedUID, leasedUID); err != nil {
		return nil, fmt.Errorf("chown chroot run dir: %w", err)
	}
	if err := os.Mkdir(chrootDevDir, 0700); err != nil && !os.IsExist(err) {
		return nil, fmt.Errorf("create chroot dev dir: %w", err)
	}
	if err := os.Chown(chrootDevDir, leasedUID, leasedUID); err != nil {
		return nil, fmt.Errorf("chown chroot dev dir: %w", err)
	}

	// Hard-link the shared kernel image into the chroot.
	kernelInChroot := filepath.Join(chrootRunDir, "vmlinux")
	if err := os.Link(f.KernelImagePath, kernelInChroot); err != nil {
		return nil, fmt.Errorf("hard-link kernel into chroot: %w", err)
	}

	// Create a block device node inside the chroot.
	rootfsInChroot := filepath.Join(chrootDevDir, "vda")
	if err := bindDeviceIntoChroot(rootfsPath, rootfsInChroot, leasedUID, leasedUID); err != nil {
		return nil, fmt.Errorf("bind rootfs device into chroot: %w", err)
	}

	// -------------------------------------------------------------------------
	// IPAM + TAP
	// -------------------------------------------------------------------------
	t1 := time.Now()
	guestIP, err := f.IPAM.Acquire()
	if err != nil {
		return nil, fmt.Errorf("failed to acquire IP: %w", err)
	}
	undoStack = append(undoStack, func() {
		f.IPAM.Release(guestIP)
	})
	log.Printf("[spawn:%s] IPAM.Acquire     %v", workerID, time.Since(t1))

	hostIP := "10.200.0.1"

	t2 := time.Now()
	tapNameLen := len(workerID)
	if tapNameLen > 8 {
		tapNameLen = 8
	}
	tapName := "tap-" + workerID[len(workerID)-tapNameLen:]
	_ = network.DeleteTap(tapName)
	if err := network.CreatePointToPointTap(tapName, hostIP, guestIP, leasedUID, leasedUID); err != nil {
		return nil, fmt.Errorf("failed to create tap device: %w", err)
	}
	undoStack = append(undoStack, func() {
		_ = network.DeleteTap(tapName)
	})
	log.Printf("[spawn:%s] TAP setup        %v", workerID, time.Since(t2))

	macByte := fmt.Sprintf("%02x", time.Now().UnixNano()%256)

	// -------------------------------------------------------------------------
	// Write Firecracker config JSON into the chroot.
	// -------------------------------------------------------------------------
	configPath := filepath.Join(chrootRunDir, fmt.Sprintf("%s.json", workerID))
	initPath := storage.DefaultGuestAgentPath
	configData := fmt.Sprintf(`{
		"boot-source": {
			"kernel_image_path": "/run/vmlinux",
			"boot_args": "console=ttyS0 reboot=k panic=1 pci=off quiet mitigations=off i8042.nokbd i8042.noaux tsc=reliable random.trust_cpu=on random.trust_bootloader=on root=/dev/vda rw rootfstype=ext4 rootflags=noload,noinit_itable init=%s herd_rootfs=1 herd_ip=%s herd_gw=%s"
		},
		"drives": [
			{
				"drive_id": "rootfs",
				"path_on_host": "/dev/vda",
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
			"uds_path": "/run/%s.sock"
		},
		"entropy": {}
	}`, initPath, guestIP, hostIP, macByte, tapName, workerID)

	if err := os.WriteFile(configPath, []byte(configData), 0644); err != nil {
		return nil, fmt.Errorf("failed to write config: %w", err)
	}

	// -------------------------------------------------------------------------
	// Launch via jailer.
	// -------------------------------------------------------------------------
	cmd := exec.Command(
		f.JailerPath,
		"--id", workerID,
		"--exec-file", f.FirecrackerPath,
		"--uid", strconv.Itoa(leasedUID),
		"--gid", strconv.Itoa(leasedUID),
		"--chroot-base-dir", f.JailerChrootBaseDir,
		"--",
		"--no-api",
		"--config-file", fmt.Sprintf("/run/%s.json", workerID),
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	t3 := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start jailer: %w", err)
	}
	undoStack = append(undoStack, func() {
		_ = cmd.Process.Kill()
	})
	log.Printf("[spawn:%s] cmd.Start        %v", workerID, time.Since(t3))

	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()

	// Wait for the VM to boot and accept vsock connections.
	t4 := time.Now()
	deadline := time.Now().Add(30 * time.Second)
	var execConn net.Conn
	var lastErr error
	socketPath := filepath.Join(chrootRunDir, fmt.Sprintf("%s.sock", workerID))
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

	workerCtx, workerCancel := context.WithCancel(context.Background())
	undoStack = append(undoStack, func() {
		workerCancel()
	})

	logPath := filepath.Join(chrootRoot, fmt.Sprintf("%s.log", workerID))
	logFile, _ := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)

	finalEnv := imgConfig.Env
	for k, v := range config.Env {
		finalEnv = append(finalEnv, fmt.Sprintf("%s=%s", k, v))
	}

	// Stream the workload payload down the vsock pipe.
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
		if err := vsock.Execute(workerCtx, execConn, payload, io.MultiWriter(os.Stdout, logFile)); err != nil {
			fmt.Printf("[host] Failed to execute payload on %s: %v\n", workerID, err)
		}
	}()

	// Create a custom HTTP client that dials over vsock for HTTP routing.
	agentClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return vsock.DialFirecracker(ctx, socketPath, 5000)
			},
		},
	}

	log.Printf("[spawn:%s] TOTAL            %v", workerID, time.Since(spawnStart))

	// Apply Port Mappings
	activeMappings := make([]PortMapping, 0, len(config.PortMappings))
	for _, pm := range config.PortMappings {
		hostPort := pm.HostPort
		if f.PortManager != nil {
			var err error
			hostPort, err = f.PortManager.Allocate(pm.HostPort, pm.Protocol, pm.HostInterface, workerID)
			if err != nil {
				return nil, fmt.Errorf("port allocation failed for mapping %d->%d: %w", pm.HostPort, pm.GuestPort, err)
			}
			// Add Port Release to stack
			capturedPort := hostPort
			undoStack = append(undoStack, func() {
				f.PortManager.Release(capturedPort)
			})
		}

		if err := network.AddPortMapping(pm.HostInterface, hostPort, guestIP, pm.GuestPort, pm.Protocol); err != nil {
			return nil, fmt.Errorf("iptables setup failed for mapping %d->%d: %w", pm.HostPort, pm.GuestPort, err)
		}
		// Capture loop variables cleanly for the closure
		capturedInterface, capturedHost, capturedGuest, capturedProto := pm.HostInterface, hostPort, pm.GuestPort, pm.Protocol
		undoStack = append(undoStack, func() {
			_ = network.RemovePortMapping(capturedInterface, capturedHost, guestIP, capturedGuest, capturedProto)
		})

		pm.HostPort = hostPort
		activeMappings = append(activeMappings, pm)
	}

	return &FirecrackerWorker{
		id:            workerID,
		socketPath:    socketPath,
		tapName:       tapName,
		guestIP:       guestIP,
		cmd:           cmd,
		client:        agentClient,
		storage:       f.Storage,
		ipam:          f.IPAM,
		chrootDir:     chrootRoot,
		done:          done,
		ctx:           workerCtx,
		cancel:        workerCancel,
		leasedUID:     leasedUID,
		uidPool:       f.UIDPool,
		portMappings:  activeMappings,
		portManager:   f.PortManager,
	}, nil
}

// bindDeviceIntoChroot creates a block device node at dstPath that mirrors
// the major/minor numbers of the host device at srcPath.
//
// This is how Firecracker (running inside the chroot as an unprivileged user)
// can open the devmapper thin-volume that containerd provisioned on the host:
// the node has the same device numbers, so the kernel routes I/O to the same
// underlying block layer.
//
// Requires CAP_MKNOD (satisfied by the daemon running as root).
func bindDeviceIntoChroot(srcPath, dstPath string, uid, gid int) error {
	var stat syscall.Stat_t
	if err := syscall.Stat(srcPath, &stat); err != nil {
		return fmt.Errorf("stat source device %s: %w", srcPath, err)
	}
	// S_IFBLK | 0600 — create as a block device. We immediately chown it
	// to the leased UID below so Firecracker can open it directly.
	if err := syscall.Mknod(dstPath, syscall.S_IFBLK|0600, int(stat.Rdev)); err != nil {
		return fmt.Errorf("mknod block device at %s: %w", dstPath, err)
	}

	if err := os.Chown(dstPath, uid, gid); err != nil {
		return fmt.Errorf("chown block device at %s: %w", dstPath, err)
	}
	return nil
}
