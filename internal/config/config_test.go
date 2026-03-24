package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validConfigYAML = `
network:
  control_socket: /tmp/herd.sock
  data_bind: 127.0.0.1:4000
worker:
  command: ["python3", "worker.py"]
  env:
    - FOO=bar
resources:
  min_workers: 1
  max_workers: 4
  memory_limit_mb: 512
  cpu_limit_cores: 1
  pids_limit: 100
  ttl: 10m
  health_interval: 5s
  worker_reuse: true
telemetry:
  log_format: json
  metrics_path: /metrics
`

func TestLoad_ValidConfig(t *testing.T) {
	t.Parallel()

	path := writeTempConfig(t, validConfigYAML)

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
	if cfg.Telemetry.LogFormat != "json" {
		t.Fatalf("expected telemetry.log_format=json, got %q", cfg.Telemetry.LogFormat)
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

	path := writeTempConfig(t, validConfigYAML+"\nextra_key: true\n")

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected yaml unknown field error")
	}
	if !strings.Contains(err.Error(), "field extra_key not found") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoad_MalformedYAML(t *testing.T) {
	t.Parallel()

	path := writeTempConfig(t, "network: [")

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected decode yaml error")
	}
	if !strings.Contains(err.Error(), "decode yaml") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoad_ValidationMatrix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		yaml       string
		expectPart string
	}{
		{
			name:       "relative socket path",
			yaml:       strings.ReplaceAll(validConfigYAML, "/tmp/herd.sock", "tmp/herd.sock"),
			expectPart: "network.control_socket must be an absolute path",
		},
		{
			name:       "non-loopback data bind",
			yaml:       strings.ReplaceAll(validConfigYAML, "127.0.0.1:4000", "0.0.0.0:4000"),
			expectPart: "host must be loopback",
		},
		{
			name:       "invalid worker env",
			yaml:       strings.ReplaceAll(validConfigYAML, "FOO=bar", "INVALID_ENV"),
			expectPart: "KEY=VALUE",
		},
		{
			name:       "invalid health path",
			yaml:       strings.Replace(validConfigYAML, "command: [\"python3\", \"worker.py\"]", "command: [\"python3\", \"worker.py\"]\n  health_path: health", 1),
			expectPart: "worker.health_path must start with '/'",
		},
		{
			name:       "invalid ttl duration",
			yaml:       strings.ReplaceAll(validConfigYAML, "ttl: 10m", "ttl: ten"),
			expectPart: "resources.ttl invalid duration",
		},
		{
			name:       "non-positive health interval",
			yaml:       strings.ReplaceAll(validConfigYAML, "health_interval: 5s", "health_interval: 0s"),
			expectPart: "resources.health_interval must be > 0",
		},
		{
			name:       "invalid log format",
			yaml:       strings.ReplaceAll(validConfigYAML, "log_format: json", "log_format: xml"),
			expectPart: "telemetry.log_format must be one of",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := writeTempConfig(t, tt.yaml)
			_, err := Load(path)
			if err == nil {
				t.Fatalf("expected error containing %q", tt.expectPart)
			}
			if !strings.Contains(err.Error(), tt.expectPart) {
				t.Fatalf("expected error containing %q, got %v", tt.expectPart, err)
			}
		})
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
