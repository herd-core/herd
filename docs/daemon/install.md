# Installation (Daemon)

The daemon mode runs as a standalone binary and is configured via a strict YAML file.

## Build

```bash
go build -o herd ./cmd/herd
```

## Minimal Config

Create a config file at `/etc/herd/config.yaml` (or any path and pass it with `--config`).

```yaml
network:
	control_socket: /tmp/herd.sock
	data_bind: 127.0.0.1:4000

worker:
	command: ["python3", "worker.py"]
	env:
		- PYTHONUNBUFFERED=1
	health_path: /health
	start_timeout: 30s
	start_health_check_delay: 1s

resources:
	target_idle: 1
	max_workers: 4
	memory_limit_mb: 512
	cpu_limit_cores: 1
	pids_limit: 100
	ttl: 10m
	health_interval: 5s
	worker_reuse: true
	insecure_sandbox: false

telemetry:
	log_format: json
	metrics_path: /metrics
```

## Run

```bash
./herd start --config /etc/herd/config.yaml
```

## Platform Guarantees

- Linux: full guarantee mode, including parent-death kill behavior for child workers.
- macOS: reduced-guarantee mode with explicit warnings; orphan prevention is best-effort.
- Other platforms: startup fails fast.

