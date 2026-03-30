package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validConfigYAML = `
network:
  control_bind: 127.0.0.1:8080
  data_bind: 127.0.0.1:4000
resources:
  max_global_vms: 100
  max_global_memory_mb: 32000
  cpu_limit_cores: 16
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
	if cfg.Network.ControlBind != "127.0.0.1:8080" {
		t.Fatalf("unexpected control bind: %q", cfg.Network.ControlBind)
	}
	if cfg.Resources.MaxGlobalVMs != 100 {
		t.Fatalf("expected max_global_vms 100, got %d", cfg.Resources.MaxGlobalVMs)
	}
	if cfg.Telemetry.LogFormat != "json" {
		t.Fatalf("expected telemetry.log_format=json, got %q", cfg.Telemetry.LogFormat)
	}
}

func TestLoad_MissingRequiredField(t *testing.T) {
	t.Parallel()

	path := writeTempConfig(t, `
network:
  control_bind: 127.0.0.1:8080
resources:
  max_global_vms: 100
  max_global_memory_mb: 32000
  cpu_limit_cores: 16
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
			name:       "invalid control bind",
			yaml:       strings.ReplaceAll(validConfigYAML, "127.0.0.1:8080", "127.0.0.1:99999"),
			expectPart: "network.control_bind invalid",
		},
		{
			name:       "non-loopback data bind",
			yaml:       strings.ReplaceAll(validConfigYAML, "127.0.0.1:4000", "0.0.0.0:4000"),
			expectPart: "host must be loopback",
		},
		{
			name:       "invalid max global vms",
			yaml:       strings.ReplaceAll(validConfigYAML, "max_global_vms: 100", "max_global_vms: 0"),
			expectPart: "resources.max_global_vms must be >= 1",
		},
		{
			name:       "invalid max global memory",
			yaml:       strings.ReplaceAll(validConfigYAML, "max_global_memory_mb: 32000", "max_global_memory_mb: 0"),
			expectPart: "resources.max_global_memory_mb must be >= 1",
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
