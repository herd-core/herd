# Herd: The Go Edge Cloud Core

**Herd** is the core engine for an Application Delivery Plane. Unlike raw microVM orchestrators that just boot virtual motherboard instances, Herd provides a complete delivery stack: holding L7 connections, cold-booting microVMs in sub-500ms, and securely tunneling traffic from standard OCI/Docker images.

## 🧱 The Engine Architecture

Herd implements four critical layers that bridge the gap between "hardware" and "application":

- **OCI Translation**: Automatically pulls Docker images via `containerd`, extracts `CMD`/`ENV`, and flattens them into Copy-on-Write rootfs snapshots.
- **L7 "Wake-on-Request" Proxy**: A high-speed reverse proxy that intercepts HTTP requests, cold-boots the corresponding VM if needed, and tunnels the traffic inside.
- **Automated IPAM**: Zero-config networking pool that manages subnets, TAP interfaces, and NAT routing.
- **Guest Agent Execution**: Injects `herd-guest-agent` as PID 1 to handle internal OS setup and workload execution.

## 🛰️ The "Brutal Difference"

| Feature | Pure Orchestrator (e.g., Raw Firecracker) | Herd (Fly.io Open Source Core) |
| :--- | :--- | :--- |
| **Input** | Custom Kernel + Raw `ext4` Disk Image | Standard Docker/OCI Image |
| **Ingress** | None. You must install Traefik/Nginx | Built-in L7 Reverse Proxy |
| **Lifecycle** | Turn On / Turn Off | Wake-on-HTTP-Request (Scale to Zero) |
| **User Experience** | Systems Engineer (Hard) | Application Developer (Easy) |

## 🛠️ Installation & Running

Herd requires **Linux with KVM** to run the Firecracker microVMs. It also requires `containerd` for storage.

### 1. Build

```bash
go build -o herd ./cmd/herd
```

### 2. Bootstrap Host Storage

```bash
# Prepare the host (loop devices, devmapper, containerd config)
sudo ./herd bootstrap --config herd.yaml
```

### 3. Start the Daemon

```bash
sudo ./herd start --config herd.yaml
```

*Note: Herd requires `sudo` for managing KVM, TAP devices, and devmapper snapshots.*

## 🔌 API Overview

### Control Plane (REST)

| Method | Endpoint | Description |
| :--- | :--- | :--- |
| `POST` | `/v1/sessions` | Acquire a new session (wakes up the VM). |
| `PUT` | `/v1/sessions/{id}/heartbeat` | Keep the session alive. |
| `GET` | `/v1/sessions/{id}/logs` | Stream real-time logs from the VM. |

### Data Plane (HTTP Proxy)

- **Port**: 8080 (Data Plane)
- **Routing Header**: `X-Session-ID`
- **Behavior**: Routes directly to the pinned VM. Supports cold-boot "Wake-on-Request" for known sessions.

## ⚙️ Configuration Reference (`herd.yaml`)

- `network.control_bind`: "127.0.0.1:8001"
- `network.data_bind`: "127.0.0.1:8080"
- `storage.state_dir`: "/var/lib/herd"
- `storage.snapshotter_name`: "devmapper"

For more details, see [CLI & Configuration Reference](./docs/daemon/cli.md).
