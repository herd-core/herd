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
	ttl: 10m
	health_interval: 5s
	worker_reuse: true

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

## Why does Herd require root (`sudo`)?

Herd inherently requires elevated privileges because it leverages hardware-level virtualization and advanced networking to isolate your workloads. Specifically:

- **Device Mapper & Block Storage (`CAP_SYS_ADMIN`)**: To provision rapid root filesystems for your MicroVMs, `herd` creates devicemapper thin-pool snapshots, loopback mounts, and filesystem images on the fly. Linux strictly requires root privileges to configure and mount these block devices.
- **Network Namespaces & TAP Interfaces (`CAP_NET_ADMIN`)**: Firecracker instances require virtual tap interfaces to communicate with the host. Creating these tap devices, binding them to a bridge, setting up IP addresses, and configuring NAT routing requires elevated networking privileges.
- **Containerd Socket Ownership**: The daemon communicates with containerd to pull and unpack container images. By default, the containerd UNIX socket is restricted so that only the root user can interact with its API.

