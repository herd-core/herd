# Herd Core

**Herd** is a high-performance microVM orchestrator that pins incoming requests to specific background workers (microVMs) using session IDs.

Built on **Firecracker**, Herd provides hardware-level isolation for stateful workloads like browsers, LLMs, and REPL servers, transforming them into multi-tenant services with sub-500ms startup latency.

## 🧱 Architecture

Herd operates a dual-plane architecture to separate management from data throughput:

- **Control Plane** (REST API): Manages session lifecycles, image warming, and log retrieval. It communicates with the host's `containerd` for storage and `firecracker` for the VM runtime.
- **Data Plane** (HTTP Proxy): A high-speed reverse proxy that routes traffic to active microVMs based on the `X-Session-ID` header.

### The Core Invariant
**1 Session ID → 1 Worker (MicroVM)**, for the lifetime of the session.

## 🚀 Key Features

- **Hardware Isolation**: Each session runs in its own Firecracker microVM, isolated by the Linux KVM hypervisor.
- **Auto-Scaling & Idle Eviction**: Dynamically scales the VM fleet and reclaims resources based on configurable TTLs.
- **Exec & Logs**: Built-in support for executing commands inside VMs via vsock and retrieving real-time logs.
- **Container-Native Storage**: Leverages OCI images and `containerd` snapshots for rapid rootfs provisioning.
- **Singleflight Acquisition**: Protects against thundering herd issues during concurrent session requests.

## 🛠️ Installation & Running

### Prerequisites
- **Linux**: Required for KVM and Firecracker support.
- **Firecracker**: Binaries must be installed on the host.
- **Containerd**: Running daemon with access to `containerd.sock`.
- **Kernel Image**: A `vmlinux.bin` file for booting microVMs.

### Build
```bash
go build -o herd ./cmd/herd
```

### Configuration (`herd.yaml`)
Herd requires a YAML configuration file to define its networking, storage, and resource limits.

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
```

### Run
```bash
sudo ./herd start --config herd.yaml
```
*Note: Herd requires `sudo` for managing TAP devices, devmapper snapshots, and containerd leases.*

## 🛠️ CLI Commands

Herd provides a unified CLI for infrastructure and session management:

- **`bootstrap`**: Prepare host loop devices and devmapper thin-pools.
- **`start`**: Launch the orchestrator and proxy planes.
- **`deploy`**: Request a new microVM session from the daemon.
- **`exec`**: Interactive shell access to a running VM.
- **`logs`**: Stream workload logs directly to your terminal.
- **`teardown`**: Purge all host state and storage.

## 🔌 API Overview

### Control Plane (REST)
- `POST /v1/sessions`: Acquire a new session (spawns a VM if needed).
- `DELETE /v1/sessions/{id}`: Terminate a session and its VM.
- `GET /v1/sessions/{id}/logs`: Retrieve VM logs.
- `POST /v1/sessions/{id}/exec`: Execute a command inside a VM (upgrades to vsock stream).
- `PUT /v1/sessions/{id}/heartbeat`: Extend session lifetime.

### Data Plane (Proxy)
- `ANY /*` with `X-Session-ID` header: Proxies traffic directly to the session's microVM.

## 📜 License

Apache 2.0 License. See `LICENSE` for details.
