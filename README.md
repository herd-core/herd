# Herd

**Herd** is a daemon and process manager that pins incoming requests to specific background workers using session IDs.


## 🧱 Daemon Mode (Primary)

Herd is built to run as a standalone daemon, providing process isolation outside of your application's memory and crash domains. By running Herd as a daemon, it handles the lifetime of stateful binaries (like Browsers, LLMs, or REPLs), effectively transforming them into multi-tenant services. Because a session always hits the same process, you can maintain in-memory state, KV caches, or local file systems without a complex coordination layer.

### The Core Invariant
**1 Session ID → 1 Worker**, for the lifetime of the session.

### Installation & Running

Build the daemon:

```bash
go build -o herd ./cmd/herd
```

Run with strict config:

```bash
./herd start --config /etc/herd/config.yaml
```

Daemon transport split:
- **Control Plane** (`network.control_socket`): Uses persistent UDS sockets as a dead-man's switch to guarantee workers are killed if your app crashes.
- **Data Plane** (`network.data_bind`): Reverse-proxies heavy HTTP/WebSocket traffic with zero overhead.

### Why It's Safe: The Dead-Man's Switch
On Linux, Herd leverages `Pdeathsig` to ensure that even if the Herd daemon itself is `kill -9`'d, the Linux kernel will instantly reap every child worker process. You never have to worry about orphaned browsers or lingering processes eating up host memory.

Platform behavior:
- Linux: full guarantee mode (Pdeathsig enabled).
- macOS: reduced-guarantee mode with explicit warnings.

### Why does Herd require root (`sudo`)?

Herd inherently requires elevated privileges because it leverages hardware-level virtualization and advanced networking to isolate your workloads. Specifically:

- **Device Mapper & Block Storage (`CAP_SYS_ADMIN`)**: To provision rapid root filesystems for your MicroVMs, `herd` creates devicemapper thin-pool snapshots, loopback mounts, and filesystem images on the fly. Linux strictly requires root privileges to configure and mount these block devices.
- **Network Namespaces & TAP Interfaces (`CAP_NET_ADMIN`)**: Firecracker instances require virtual tap interfaces to communicate with the host. Creating these tap devices, binding them to a bridge, setting up IP addresses, and configuring NAT routing requires elevated networking privileges.
- **Containerd Socket Ownership**: The daemon communicates with containerd to pull and unpack container images. By default, the containerd UNIX socket is restricted so that only the root user can interact with its API.

For comprehensive daemon docs, see:
- `docs/daemon/install.md`
- `docs/daemon/cli.md`
- `docs/daemon/uds.md`


## 🚀 Key Features

- **Session Affinity**: Guaranteed routing of a session ID to its unique pinned worker.
- **Auto-Scaling**: Dynamically scale workers between `min` and `max` bounds based on demand.
- **Idle Eviction (TTL)**: Automatically reclaim workers that haven't been accessed within a configurable TTL.
- **Health Monitoring**: Continuous liveness checks on every worker process; dead workers are automatically replaced.
- **Singleflight Acquisition**: Protects against "thundering herd" issues where multiple concurrent requests for a new session ID try to spawn workers simultaneously.
- **Generic Clients**: Fully generic `Pool[C]` supports any client type (HTTP, gRPC, custom structs).
- **Reverse Proxy Helper**: Built-in HTTP reverse proxy that handles the full session lifecycle (Acquire → Proxy → Release).

---

### Feature Comparison
| Feature | `herd` | Kubernetes | PM2 |
|---|---|---|---|
| Startup latency | <100ms | 2s – 10s | 500ms+ |
| Session affinity | ✅ Native (Session ID) | ⚠️ Complex (Sticky Sessions) | ❌ None |
| Footprint | Single binary, zero deps | Massive control plane | Node.js runtime required |
| Programming model | YAML config driven | YAML / REST API | CLI / JS config |
| Crash + cleanup | ✅ OS-level guarantee | ⚠️ pod restart only | ⚠️ restart only |
| Built-in HTTP proxy | ✅ Native | ❌ separate Ingress concern | ❌ |


### Existing OSS Landscape
| Project | Multi-process pool | Named session routing | Crash + cleanup | License | Language |
|---|---|---|---|---|---|
| Browserless | ✅ | ❌ WebSocket-sticky | ✅ | SSPL | TypeScript |
| puppeteer-cluster | ✅ | ❌ stateless tasks | ✅ | MIT | TypeScript |
| PM2 / Supervisord | ✅ | ❌ none | ⚠️ | MIT/BSD | Python/JS |
| Selenium Grid | ✅ | ✅ WebDriver-specific | ✅ | Apache 2.0 | Java |
| E2B infra | ✅ (VMs) | ✅ | ✅ | Apache 2.0 | Go (cloud-only) |
| **herd** | ✅ | ✅ explicit ID routing | ✅ | **MIT** | **Go** |


## 📦 Go Library Mode (Secondary)

While Herd is primarily designed to run as a standalone daemon, you can still embed it directly into your own Go applications. See the [Embedded Library Documentation](./docs/go/embedded-library.md) for Go examples.

## 🔁 Migration: Embedded Library to Daemon

If you previously embedded Herd in your app (`herd.New(...)` + in-process proxy), migrate to:

1. Run Herd daemon as a separate process.
2. Acquire/hold session via control stream over UDS.
3. Send workload HTTP traffic to the daemon data plane with session header (`X-Session-ID`).

Important semantic shift:

- In daemon mode, control stream liveness owns session lifetime.
- When the control stream closes/errors, the daemon force-kills that session's worker.

---



## ⚙️ Configuration Options (`herd.yaml`)

| Option | Description |
| :--- | :--- |
| `network.control_socket` | UDS socket path for the Control Plane (e.g., `/tmp/herd.sock`). |
| `network.data_bind` | IP:Port for the Data Plane HTTP proxy (e.g., `127.0.0.1:8080`). |
| `worker.command` | The subprocess command and args to spawn (e.g., `["npx", "playwright", "run-server"]`). |
| `worker.env` | Environment variables to inject (`FOO=bar`). Supports templating like `{{.Port}}`. |
| `resources.target_idle` / `max_workers` | Sets the auto-scaling floor and ceiling for the process fleet. |
| `resources.ttl` | Max idle time for a session before the worker is automatically evicted (e.g. `15m`). |
| `resources.worker_reuse` | Whether to recycle workers for new sessions or kill them when TTL expires. |
| `resources.health_interval` | How often to poll worker `/healthz` endpoints. |
| `resources.memory_limit_mb` | (Linux) cgroups-based hard memory limit per worker. |
| `resources.cpu_limit_cores` | (Linux) cgroups-based CPU slicing per worker. |

---

##  License

MIT License. See `LICENSE` for details.
