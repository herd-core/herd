# Herd: The Go Edge Cloud Core

**Herd** is the core engine for an Application Delivery Plane. Unlike raw microVM orchestrators that just boot virtual motherboard instances, Herd provides a complete delivery stack: holding L7 connections, cold-booting microVMs in sub-500ms, and securely tunneling traffic from standard OCI/Docker images.

## 🧱 The Engine Architecture

Herd implements four critical layers that bridge the gap between "hardware" and "application":

- **OCI Translation**: Automatically pulls Docker images via `containerd`, extracts `CMD`/`ENV`, and flattens them into Copy-on-Write rootfs snapshots.
- **L7 "Wake-on-Request" Proxy**: A high-speed reverse proxy that intercepts HTTP requests, cold-boots the corresponding VM (if needed [WIP]), and tunnels the traffic inside.
- **Automated IPAM**: Zero-config networking pool that manages subnets, TAP interfaces, and NAT routing.
- **Guest Agent Execution**: Injects `herd-guest-agent` as PID 1 to handle internal OS setup and workload execution.
- **Port Publishing (Hybrid Mode)**: Supports both deterministic host port binding (`-p 80:80`) and dynamic ephemeral allocation for secure, multi-tenant ingress.
- **Dynamic UID Isolation**: Leverages the Firecracker jailer to drop process privileges into a unique, per-VM UID/GID leased from a dynamic pool. This ensures every tenant runs in a distinct DAC security domain, providing absolute lateral movement protection on multi-tenant hosts.

## 🛰️ The "Brutal Difference"

| Feature | Pure Orchestrator (e.g., Raw Firecracker) | Herd (Fly.io Open Source Core) |
| :--- | :--- | :--- |
| **Input** | Custom Kernel + Raw `ext4` Disk Image | Standard Docker/OCI Image |
| **Ingress** | None. You must install Traefik/Nginx | Built-in L7 Reverse Proxy |
| **Lifecycle** | Turn On / Turn Off | Wake-on-HTTP-Request (Scale to Zero) [WIP] |
| **User Experience** | Systems Engineer (Hard) | Application Developer (Easy) |

## 🛠️ Installation & Running

### 1. Prerequisites

- **Host OS**: Linux (A recent kernel with KVM support).
- **Virtualization**: Hardware virtualization (VT-x or AMD-V) must be enabled in the BIOS/UEFI.
- **KVM Access**: The `/dev/kvm` device must exist and be accessible.
    ```bash
    ls -l /dev/kvm
    ```
- **Root Access**: Most `herd` commands require `sudo`.

Before installing Herd, ensure your system has `containerd` and `iptables` installed:

```bash
sudo apt update && sudo apt install -y containerd iptables
```

### 2. Quick Install

```bash
curl -sSL https://raw.githubusercontent.com/herd-core/herd/main/scripts/install.sh | bash
```

### 2. Initialize Host

```bash
# Prepare the host (loop devices, devmapper, containerd config)
sudo herd init

# Or in non-interactive mode:
sudo herd init --yes
```


### 3. Start the Daemon

```bash
sudo herd start
```

### 4. Deploy a MicroVM

```bash
herd deploy --image nginx:latest
```

*Note: Herd requires `sudo` for managing KVM, TAP devices, and devmapper snapshots.*

## 🔌 API Overview

### Control Plane (REST)

| Method | Endpoint | Description |
| :--- | :--- | :--- |
| `POST` | `/v1/sessions` | Acquire a new session (wakes up the VM). |
| `GET` | `/v1/sessions` | List all active sessions. |
| `DELETE` | `/v1/sessions/{id}` | Kill a running session. |
| `PUT` | `/v1/sessions/{id}/heartbeat` | Keep the session alive. |
| `GET` | `/v1/sessions/{id}/logs` | Stream real-time logs from the VM. |

### Data Plane (HTTP Proxy)

- **Port**: 8080 (Data Plane)
- **Routing Header**: `X-Session-ID`
- **Behavior**: Routes directly to the pinned VM. Supports cold-boot "Wake-on-Request" for known sessions.

## ⚙️ Configuration Reference (`herd.yaml`)

- `network.control_bind`: "127.0.0.1:8081"
- `network.data_bind`: "127.0.0.1:8080"
- `storage.state_dir`: "/var/lib/herd"
- `storage.snapshotter_name`: "devmapper"
- `binaries.jailer_path`: "/usr/local/bin/jailer"
- `jailer.uid_pool_start`: 300000
- `jailer.uid_pool_size`: 200
- `jailer.chroot_base_dir`: "/srv/jailer"

For more details, see [CLI & Configuration Reference](./docs/cli.md).
