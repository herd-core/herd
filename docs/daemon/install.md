# Installation (Daemon)

The Herd daemon runs as a standalone binary and is configured via a strict YAML file. It requires root privileges to manage microVMs, networking, and storage.

See also:
- [Host Dependencies](../dependencies.md)

## Build

```bash
go build -o herd ./cmd/herd
```

## Prerequisites

- **Firecracker**: Ensure the `firecracker` binary is available at the path specified in your config or a standard location.
- **Containerd**: A running `containerd` instance is required for image management.
- **Kernel Image**: A `vmlinux.bin` file is needed to boot the microVMs.
- **KVM**: The host must support and have KVM enabled (`/dev/kvm`).

## Minimal Config

Create a config file (e.g., `herd.yaml`). Note that there is no default; all required fields must be specified.

```yaml
network:
  control_bind: "127.0.0.1:8001"
  data_bind: "127.0.0.1:8080"

storage:
  state_dir: "/var/lib/herd"
  namespace: "herd"
  snapshotter_name: "devmapper"

resources:
  max_global_vms: 50
  max_global_memory_mb: 20480
  cpu_limit_cores: 4

telemetry:
  log_format: "json"
  metrics_path: "/metrics"
```

## Run

```bash
sudo ./herd start --config herd.yaml
```

## Why does Herd require root (`sudo`)?

Herd requires elevated privileges to leverage hardware-level virtualization and advanced networking:

- **Block Storage (`CAP_SYS_ADMIN`)**: To provision root filesystems, Herd creates devmapper thin-pool snapshots and mounts them.
- **Networking (`CAP_NET_ADMIN`)**: Creating TAP interfaces, binding them to bridges, and configuring NAT routing requires networking privileges.
- **Containerd**: Interaction with the `containerd.sock` often requires root access by default.

## Platform Guarantees

- **Linux**: Full support for Firecracker, KVM, and high-performance networking.
- **macOS/Other**: Not supported for Firecracker mode (requires KVM).
