# CLI Reference

## Command

```bash
herd start --config /etc/herd/config.yaml
```

## Flags

- `--config`: path to daemon YAML config (default: `/etc/herd/config.yaml`).

The daemon is strict config-first. Unknown YAML keys and invalid values fail startup.

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
- Data plane: HTTP on `network.data_bind`.

## Session Lifecycle in Daemon Mode

- Clients establish a control stream via `Acquire` over UDS.
- Daemon allocates one pinned session per stream.
- On stream close/error, daemon force-kills that session's worker.

This behavior is intentional for stateful workload cleanup and crash containment.

