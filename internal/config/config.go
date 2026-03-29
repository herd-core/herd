package config

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the strict daemon bootstrap contract.
// The daemon fails fast if any required field is missing or malformed.
type Config struct {
	Network   NetworkConfig   `yaml:"network"`
	Storage   StorageConfig   `yaml:"storage"`
	Worker    WorkerConfig    `yaml:"worker"`
	Resources ResourceConfig  `yaml:"resources"`
	Telemetry TelemetryConfig `yaml:"telemetry"`
}

type StorageConfig struct {
	StateDir        string `yaml:"state_dir"`
	SnapshotterName string `yaml:"snapshotter_name"`
	Namespace       string `yaml:"namespace"`
}

type NetworkConfig struct {
	ControlBind string `yaml:"control_bind"`
	DataBind      string `yaml:"data_bind"`
}

type WorkerConfig struct {
	Command               []string `yaml:"command"`
	Env                   []string `yaml:"env"`
	HealthPath            string   `yaml:"health_path"`
	StartTimeout          string   `yaml:"start_timeout"`
	StartHealthCheckDelay string   `yaml:"start_health_check_delay"`
}

func (w WorkerConfig) StartTimeoutDuration() time.Duration {
	d, _ := time.ParseDuration(w.StartTimeout)
	return d
}

func (w WorkerConfig) StartHealthCheckDelayDuration() time.Duration {
	d, _ := time.ParseDuration(w.StartHealthCheckDelay)
	return d
}

type ResourceConfig struct {
	TargetIdle     int     `yaml:"target_idle"`
	MaxWorkers     int     `yaml:"max_workers"`
	MemoryLimitMB  int64   `yaml:"memory_limit_mb"`
	CPULimitCores  float64 `yaml:"cpu_limit_cores"`
	TTL            string  `yaml:"ttl"`             // Maps to IdleTTL
	AbsoluteTTL    string  `yaml:"absolute_ttl"`    // Max session duration
	HeartbeatGrace string  `yaml:"heartbeat_grace"` // Max time without ping
	DataTimeout    string  `yaml:"data_timeout"`    // Max time for HTTP request
	HealthInterval string  `yaml:"health_interval"`
}

type TelemetryConfig struct {
	LogFormat   string `yaml:"log_format"`
	MetricsPath string `yaml:"metrics_path"`
}

func (r ResourceConfig) IdleTTLDuration() time.Duration {
	d, _ := time.ParseDuration(r.TTL)
	return d
}

func (r ResourceConfig) AbsoluteTTLDuration() time.Duration {
	d, _ := time.ParseDuration(r.AbsoluteTTL)
	return d
}

func (r ResourceConfig) HeartbeatGraceDuration() time.Duration {
	d, _ := time.ParseDuration(r.HeartbeatGrace)
	return d
}

func (r ResourceConfig) DataTimeoutDuration() time.Duration {
	d, _ := time.ParseDuration(r.DataTimeout)
	return d
}

func (r ResourceConfig) HealthIntervalDuration() time.Duration {
	d, _ := time.ParseDuration(r.HealthInterval)
	return d
}

func (r ResourceConfig) MemoryLimitBytes() int64 {
	return r.MemoryLimitMB * 1024 * 1024
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
	if c.Worker.HealthPath == "" {
		c.Worker.HealthPath = "/health"
	}
	if c.Worker.StartTimeout == "" {
		c.Worker.StartTimeout = "30s"
	}
	if c.Worker.StartHealthCheckDelay == "" {
		c.Worker.StartHealthCheckDelay = "1s"
	}
	if c.Resources.HealthInterval == "" {
		c.Resources.HealthInterval = "5s"
	}
	if c.Resources.TTL == "" {
		c.Resources.TTL = "5m"
	}
	if c.Telemetry.LogFormat == "" {
		c.Telemetry.LogFormat = "json"
	}
	if c.Telemetry.MetricsPath == "" {
		c.Telemetry.MetricsPath = "/metrics"
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
	if len(c.Worker.Command) == 0 || c.Worker.Command[0] == "" {
		return fmt.Errorf("worker.command must include at least one entry (binary)")
	}
	for i, arg := range c.Worker.Command {
		if strings.TrimSpace(arg) == "" {
			return fmt.Errorf("worker.command[%d] must not be empty", i)
		}
	}
	if !strings.HasPrefix(c.Worker.HealthPath, "/") {
		return fmt.Errorf("worker.health_path must start with '/'")
	}
	for i, envKV := range c.Worker.Env {
		if strings.TrimSpace(envKV) == "" {
			return fmt.Errorf("worker.env[%d] must not be empty", i)
		}
		eq := strings.Index(envKV, "=")
		if eq <= 0 {
			return fmt.Errorf("worker.env[%d] must be in KEY=VALUE format", i)
		}
	}
	if c.Resources.TargetIdle < 1 {
		return fmt.Errorf("resources.target_idle must be >= 1")
	}
	if c.Resources.MaxWorkers < c.Resources.TargetIdle {
		return fmt.Errorf("resources.max_workers must be >= resources.target_idle")
	}
	if c.Resources.MemoryLimitMB < 1 {
		return fmt.Errorf("resources.memory_limit_mb must be >= 1")
	}
	if c.Resources.CPULimitCores < 0 {
		return fmt.Errorf("resources.cpu_limit_cores must be >= 0")
	}
	if d, err := time.ParseDuration(c.Worker.StartTimeout); err != nil {
		return fmt.Errorf("worker.start_timeout invalid duration: %w", err)
	} else if d <= 0 {
		return fmt.Errorf("worker.start_timeout must be > 0")
	}
	if d, err := time.ParseDuration(c.Worker.StartHealthCheckDelay); err != nil {
		return fmt.Errorf("worker.start_health_check_delay invalid duration: %w", err)
	} else if d < 0 {
		return fmt.Errorf("worker.start_health_check_delay must be >= 0")
	}
	if d, err := time.ParseDuration(c.Resources.TTL); err != nil {
		return fmt.Errorf("resources.ttl invalid duration: %w", err)
	} else if d <= 0 {
		return fmt.Errorf("resources.ttl must be > 0")
	}
	if d, err := time.ParseDuration(c.Resources.HealthInterval); err != nil {
		return fmt.Errorf("resources.health_interval invalid duration: %w", err)
	} else if d <= 0 {
		return fmt.Errorf("resources.health_interval must be > 0")
	}
	if c.Telemetry.LogFormat != "json" && c.Telemetry.LogFormat != "text" {
		return fmt.Errorf("telemetry.log_format must be one of: json, text")
	}
	if !strings.HasPrefix(c.Telemetry.MetricsPath, "/") {
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
