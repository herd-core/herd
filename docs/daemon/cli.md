# CLI & Configuration Reference

## Command

```bash
herd start --config /etc/herd/config.yaml
```

## Flags

- `--config`: path to daemon YAML config (default: `/etc/herd/config.yaml`).

The daemon is strict config-first. Unknown YAML keys and invalid values fail startup.

## Configuration Options (`herd.yaml`)

Your `herd.yaml` file tells Herd how to spawn workers and expose them securely.

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
  min_workers: 1
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

### Reference

| Option | Description |
| :--- | :--- |
| `network.control_socket` | UDS socket path for the Control Plane (e.g., `/tmp/herd.sock`). |
| `network.data_bind` | IP:Port for the Data Plane HTTP proxy (e.g., `127.0.0.1:8080`). |
| `worker.command` | The subprocess command and args to spawn (e.g., `["npx", "playwright", "run-server"]`). |
| `worker.env` | Environment variables to inject (`FOO=bar`). Supports templating like `{{.Port}}`. |
| `worker.health_path` | HTTP path polled for liveness. |
| `worker.start_timeout` | Time allowed for worker to become healthy. |
| `resources.min_workers` / `max_workers` | Sets the auto-scaling floor and ceiling for the process fleet. |
| `resources.ttl` | Max idle time for a session before the worker is automatically evicted (e.g. `15m`). |
| `resources.worker_reuse` | Whether to recycle workers for new sessions or kill them when TTL expires. |
| `resources.health_interval` | How often to poll worker health endpoints. |
| `resources.memory_limit_mb` | (Linux) cgroups-based hard memory limit per worker. |
| `resources.cpu_limit_cores` | (Linux) cgroups-based CPU slicing per worker. |
| `resources.pids_limit` | (Linux) cgroups-based PID limits. |

## Startup Contract

On successful start, `herd start`:

1. Validates runtime policy (Linux full guarantees, macOS reduced guarantees).
2. Loads and validates config.
3. Bootstraps worker pool from config.
4. Starts control plane gRPC on Unix socket.
5. Starts data plane HTTP server on local TCP bind.

Any failure in these steps terminates startup immediately.

## Control/Data Split

- Control plane: gRPC on `network.control_socket` (Unix Domain Socket).
- Data plane: HTTP on `network.data_bind` (Reverse proxy to active workers).

## Session Lifecycle in Daemon Mode

- Clients establish a control stream via `Acquire` over UDS.
- Daemon allocates one pinned session per stream.
- On stream close/error, daemon force-kills that session's worker.

This behavior is intentional for stateful workload cleanup and crash containment.

