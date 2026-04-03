package config

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/user"
	"strconv"

	"gopkg.in/yaml.v3"
)

// Version is the current version of herd.
// Version is the current version of herd.
const Version = "v0.5.0"

// GetTargetHomeDir returns the home directory of the original user if running under sudo,
// falling back to the current user's home directory.
func GetTargetHomeDir() (string, error) {
	if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" {
		if u, err := user.Lookup(sudoUser); err == nil {
			return u.HomeDir, nil
		}
	}
	return os.UserHomeDir()
}

// Config is the strict daemon bootstrap contract.
// The daemon fails fast if any required field is missing or malformed.
type Config struct {
	Network   NetworkConfig   `yaml:"network"`
	Storage   StorageConfig   `yaml:"storage"`
	Resources ResourceConfig  `yaml:"resources"`
	Binaries  BinaryConfig    `yaml:"binaries"`
	Jailer    JailerConfig    `yaml:"jailer"`
	Telemetry TelemetryConfig `yaml:"telemetry"`
	Cloud     CloudConfig     `yaml:"cloud"`
}

type CloudConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Endpoint string `yaml:"endpoint"`
	NodeID   string `yaml:"node_id"`
}

type BinaryConfig struct {
	FirecrackerPath string `yaml:"firecracker_path"`
	JailerPath      string `yaml:"jailer_path"`
	KernelImagePath string `yaml:"kernel_image_path"`
	GuestAgentPath  string `yaml:"guest_agent_path"`
}

// JailerConfig holds parameters for the Firecracker jailer process.
//
// Each concurrent MicroVM is assigned a unique UID/GID leased from the pool
// [UIDPoolStart, UIDPoolStart+UIDPoolSize). This ensures every tenant runs in
// a distinct DAC security domain — a requirement for multi-tenant public cloud
// deployments where different tenants share the same bare-metal host.
type JailerConfig struct {
	// UIDPoolStart is the first UID (and GID) in the pool. Must be >= 65536 to
	// stay well above system-reserved UIDs. Recommended: 300000.
	UIDPoolStart  int    `yaml:"uid_pool_start"`
	// UIDPoolSize is how many concurrent MicroVMs the pool can support.
	// Set this to at least your max_global_vms value.
	UIDPoolSize   int    `yaml:"uid_pool_size"`
	ChrootBaseDir string `yaml:"chroot_base_dir"`
}

type StorageConfig struct {
	StateDir        string `yaml:"state_dir"`
	SnapshotterName string `yaml:"snapshotter_name"`
	Namespace       string `yaml:"namespace"`
}

type NetworkConfig struct {
	ControlBind        string `yaml:"control_bind"`
	DataBind           string `yaml:"data_bind"`
	EphemeralPortStart int    `yaml:"ephemeral_port_start"`
	EphemeralPortEnd   int    `yaml:"ephemeral_port_end"`
}

type ResourceConfig struct {
	MaxGlobalVMs       int     `yaml:"max_global_vms"`
	MaxGlobalMemoryMB  int64   `yaml:"max_global_memory_mb"`
	CPULimitCores      float64 `yaml:"cpu_limit_cores"`
}

type TelemetryConfig struct {
	LogFormat   string `yaml:"log_format"`
	MetricsPath string `yaml:"metrics_path"`
}

func (r ResourceConfig) MemoryLimitBytes() int64 {
	return r.MaxGlobalMemoryMB * 1024 * 1024
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode yaml: %w", err)
	}

	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Telemetry.LogFormat == "" {
		c.Telemetry.LogFormat = "json"
	}
	if c.Telemetry.MetricsPath == "" {
		c.Telemetry.MetricsPath = "/metrics"
	}
	if c.Network.EphemeralPortStart == 0 {
		c.Network.EphemeralPortStart = 10000
	}
	if c.Network.EphemeralPortEnd == 0 {
		c.Network.EphemeralPortEnd = 39999
	}
}

func (c *Config) Validate() error {
	if c.Network.ControlBind == "" {
		return fmt.Errorf("network.control_bind is required")
	}
	if err := validateDataBind(c.Network.ControlBind); err != nil {
		return fmt.Errorf("network.control_bind invalid: %w", err)
	}
	if c.Network.DataBind == "" {
		return fmt.Errorf("network.data_bind is required")
	}
	if err := validateDataBind(c.Network.DataBind); err != nil {
		return fmt.Errorf("network.data_bind invalid: %w", err)
	}
	if c.Network.EphemeralPortStart < 1 || c.Network.EphemeralPortStart > 65535 {
		return fmt.Errorf("network.ephemeral_port_start must be between 1 and 65535")
	}
	if c.Network.EphemeralPortEnd < 1 || c.Network.EphemeralPortEnd > 65535 {
		return fmt.Errorf("network.ephemeral_port_end must be between 1 and 65535")
	}
	if c.Network.EphemeralPortStart > c.Network.EphemeralPortEnd {
		return fmt.Errorf("network.ephemeral_port_start must be <= network.ephemeral_port_end")
	}
	if c.Resources.MaxGlobalVMs < 1 {
		return fmt.Errorf("resources.max_global_vms must be >= 1")
	}
	if c.Resources.MaxGlobalMemoryMB < 1 {
		return fmt.Errorf("resources.max_global_memory_mb must be >= 1")
	}
	if c.Resources.CPULimitCores < 0 {
		return fmt.Errorf("resources.cpu_limit_cores must be >= 0")
	}
	if c.Telemetry.LogFormat != "json" && c.Telemetry.LogFormat != "text" {
		return fmt.Errorf("telemetry.log_format must be one of: json, text")
	}
	if c.Binaries.FirecrackerPath == "" {
		return fmt.Errorf("binaries.firecracker_path is required")
	}
	if c.Binaries.JailerPath == "" {
		return fmt.Errorf("binaries.jailer_path is required")
	}
	if c.Binaries.KernelImagePath == "" {
		return fmt.Errorf("binaries.kernel_image_path is required")
	}
	if c.Binaries.GuestAgentPath == "" {
		return fmt.Errorf("binaries.guest_agent_path is required")
	}
	if c.Jailer.UIDPoolStart < 65536 {
		return fmt.Errorf("jailer.uid_pool_start must be >= 65536 (got %d): values below 65536 overlap system-reserved UIDs", c.Jailer.UIDPoolStart)
	}
	if c.Jailer.UIDPoolSize < 1 {
		return fmt.Errorf("jailer.uid_pool_size must be >= 1 (got %d)", c.Jailer.UIDPoolSize)
	}
	if c.Jailer.ChrootBaseDir == "" {
		return fmt.Errorf("jailer.chroot_base_dir is required")
	}
	if c.Telemetry.MetricsPath == "" || c.Telemetry.MetricsPath[0] != '/' {
		return fmt.Errorf("telemetry.metrics_path must start with '/'")
	}
	return nil
}

func validateDataBind(bind string) error {
	host, portStr, err := net.SplitHostPort(bind)
	if err != nil {
		return fmt.Errorf("must be host:port: %w", err)
	}

	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}

	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("host must be loopback (localhost, 127.0.0.1, or ::1)")
	}

	return nil
}
