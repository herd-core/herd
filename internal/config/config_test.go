package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad_ValidConfig(t *testing.T) {
	t.Parallel()

	path := writeTempConfig(t, `
network:
  control_socket: /tmp/herd.sock
  data_bind: 127.0.0.1:4000
worker:
  command: ["python3", "worker.py"]
resources:
  min_workers: 1
  max_workers: 4
  memory_limit_mb: 512
  cpu_limit_cores: 1
  pids_limit: 100
  ttl: 10m
  health_interval: 5s
  worker_reuse: true
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Network.ControlSocket != "/tmp/herd.sock" {
		t.Fatalf("unexpected control socket: %q", cfg.Network.ControlSocket)
	}
	if got := cfg.Worker.StartTimeoutDuration().String(); got != "30s" {
		t.Fatalf("expected default start_timeout 30s, got %q", got)
	}
}

func TestLoad_MissingRequiredField(t *testing.T) {
	t.Parallel()

	path := writeTempConfig(t, `
network:
  control_socket: /tmp/herd.sock
worker:
  command: ["python3", "worker.py"]
resources:
  min_workers: 1
  max_workers: 4
  memory_limit_mb: 512
  cpu_limit_cores: 1
  pids_limit: 100
  ttl: 10m
  health_interval: 5s
  worker_reuse: true
`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for missing network.data_bind")
	}
	if !strings.Contains(err.Error(), "network.data_bind") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoad_UnknownFieldFails(t *testing.T) {
	t.Parallel()

	path := writeTempConfig(t, `
network:
  control_socket: /tmp/herd.sock
  data_bind: 127.0.0.1:4000
worker:
  command: ["python3", "worker.py"]
resources:
  min_workers: 1
  max_workers: 4
  memory_limit_mb: 512
  cpu_limit_cores: 1
  pids_limit: 100
  ttl: 10m
  health_interval: 5s
  worker_reuse: true
extra_key: true
`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected yaml unknown field error")
	}
	if !strings.Contains(err.Error(), "field extra_key not found") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func writeTempConfig(t *testing.T, contents string) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "herd.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return path
}
