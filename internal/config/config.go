package config

import (
	"bytes"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the strict daemon bootstrap contract.
// The daemon fails fast if any required field is missing or malformed.
type Config struct {
	Network   NetworkConfig  `yaml:"network"`
	Worker    WorkerConfig   `yaml:"worker"`
	Resources ResourceConfig `yaml:"resources"`
}

type NetworkConfig struct {
	ControlSocket string `yaml:"control_socket"`
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
	MinWorkers     int     `yaml:"min_workers"`
	MaxWorkers     int     `yaml:"max_workers"`
	MemoryLimitMB  int64   `yaml:"memory_limit_mb"`
	CPULimitCores  float64 `yaml:"cpu_limit_cores"`
	PIDsLimit      int64   `yaml:"pids_limit"`
	TTL            string  `yaml:"ttl"`
	HealthInterval string  `yaml:"health_interval"`
	WorkerReuse    bool    `yaml:"worker_reuse"`

	// InsecureSandbox enables reduced sandboxing for local development.
	// This should not be enabled in production.
	InsecureSandbox bool `yaml:"insecure_sandbox"`
}

func (r ResourceConfig) TTLDuration() time.Duration {
	d, _ := time.ParseDuration(r.TTL)
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
}

func (c *Config) Validate() error {
	if c.Network.ControlSocket == "" {
		return fmt.Errorf("network.control_socket is required")
	}
	if c.Network.DataBind == "" {
		return fmt.Errorf("network.data_bind is required")
	}
	if len(c.Worker.Command) == 0 || c.Worker.Command[0] == "" {
		return fmt.Errorf("worker.command must include at least one entry (binary)")
	}
	if c.Resources.MinWorkers < 1 {
		return fmt.Errorf("resources.min_workers must be >= 1")
	}
	if c.Resources.MaxWorkers < c.Resources.MinWorkers {
		return fmt.Errorf("resources.max_workers must be >= resources.min_workers")
	}
	if c.Resources.MemoryLimitMB < 1 {
		return fmt.Errorf("resources.memory_limit_mb must be >= 1")
	}
	if c.Resources.CPULimitCores < 0 {
		return fmt.Errorf("resources.cpu_limit_cores must be >= 0")
	}
	if c.Resources.PIDsLimit == 0 || c.Resources.PIDsLimit < -1 {
		return fmt.Errorf("resources.pids_limit must be > 0 or -1")
	}
	if _, err := time.ParseDuration(c.Worker.StartTimeout); err != nil {
		return fmt.Errorf("worker.start_timeout invalid duration: %w", err)
	}
	if _, err := time.ParseDuration(c.Worker.StartHealthCheckDelay); err != nil {
		return fmt.Errorf("worker.start_health_check_delay invalid duration: %w", err)
	}
	if _, err := time.ParseDuration(c.Resources.TTL); err != nil {
		return fmt.Errorf("resources.ttl invalid duration: %w", err)
	}
	if _, err := time.ParseDuration(c.Resources.HealthInterval); err != nil {
		return fmt.Errorf("resources.health_interval invalid duration: %w", err)
	}
	return nil
}
