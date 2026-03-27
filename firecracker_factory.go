package herd

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
"github.com/herd-core/herd/internal/storage"

	"time"
)

// FirecrackerFactory is a minimal implementation of a WorkerFactory that spawns Firecracker VMs.
type FirecrackerFactory struct {
	FirecrackerPath string
	KernelImagePath string
	Storage *storage.Manager

	SocketPathDir   string
}

// FirecrackerWorker represents a single running Firecracker VM.
type FirecrackerWorker struct {
	storage *storage.Manager
	id         string
	socketPath string
	cmd        *exec.Cmd
	client     *http.Client
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

	// 2. The Real Check: Ping the guest agent running inside the VM.
	// Once VSOCK is configured, f.client will route this HTTP request
	// over vsock into the microVM's listening server.
	req, err := http.NewRequestWithContext(ctx, "GET", "http://worker/health", nil)
	if err != nil {
		return err
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return fmt.Errorf("agent inside vm is not reachable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("agent returned status %d", resp.StatusCode)
	}

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

	if f.storage != nil {
		f.storage.Teardown(context.Background(), f.id)
	}
	return nil
}

// Spawn starts a new Firecracker VM.
func (f *FirecrackerFactory) Spawn(ctx context.Context) (Worker[*http.Client], error) {
	workerID := fmt.Sprintf("fc-%d", time.Now().UnixNano())
	socketPath := filepath.Join(f.SocketPathDir, fmt.Sprintf("%s.sock", workerID))

	rootfsPath, err := f.Storage.PullAndSnapshot(ctx, "docker.io/library/ubuntu:latest", workerID)
	if err != nil {
		return nil, fmt.Errorf("failed to pull and snapshot rootfs: %w", err)
	}

	// Ensure old socket is removed
	os.Remove(socketPath)

	// In a real implementation we would interact via the API socket
	// but for now we just start the process and configure via CLI

	// Create a minimal config json
	configPath := filepath.Join(f.SocketPathDir, fmt.Sprintf("%s.json", workerID))
	configData := fmt.Sprintf(`{
		"boot-source": {
			"kernel_image_path": "%s",
			"boot_args": "console=ttyS0 reboot=k panic=1 pci=off"
		},
		"drives": [
			{
				"drive_id": "rootfs",
				"path_on_host": "%s",
				"is_root_device": true,
				"is_read_only": false
			}
		],
		"machine-config": {
			"vcpu_count": 1,
			"mem_size_mib": 128
		}
	}`, f.KernelImagePath, rootfsPath)

	if err := os.WriteFile(configPath, []byte(configData), 0644); err != nil {
		return nil, fmt.Errorf("failed to write config: %w", err)
	}

	// Start firecracker with the config
	// `--no-api` tells Firecracker to autoboot the machine using the provided config immediately
	cmd := exec.CommandContext(ctx, f.FirecrackerPath, "--no-api", "--config-file", configPath)

	// In a real scenario we'd pipe stdout/err to a logger
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start firecracker: %w", err)
	}

	// Wait for the process in the background so cmd.ProcessState is populated on crash
	go func() {
		_ = cmd.Wait()
	}()

	// We'd normally wait for the socket/guest agent to come up here.
	// We'll just sleep briefly for now for this minimal implementation.
	time.Sleep(500 * time.Millisecond)

	return &FirecrackerWorker{
		id:         workerID,
		socketPath: socketPath,
		cmd:        cmd,
		client:     &http.Client{}, // Default client, would need custom dialer for vsock
		storage: f.Storage,
	}, nil
}
