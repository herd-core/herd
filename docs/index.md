# What is Herd?

> "Kubernetes is too slow to spawn sessions. Redis-only maps are too complex to maintain."

**Herd** is a daemon and process manager that pins incoming requests to specific background workers using session IDs.

> Build features, not infrastructure

### The Core Invariant
**1 Session ID → 1 Worker**, for the lifetime of the session.

This invariant transforms stateful binaries (like Browsers, LLMs, or REPLs) into multi-tenant services. Because a session always hits the same process, you can maintain in-memory state, KV caches, or local file systems without a complex coordination layer.

## 🧱 Daemon Mode (Primary)

Herd is built to run as a standalone daemon, providing process isolation outside of your application's memory and crash domains. By running Herd as a daemon, it handles the lifetime of stateful binaries, handling lifecycle, process limits, routing, and reverse-proxying.

Daemon transport split:
- **Control Plane** (`network.control_socket`): Uses persistent UDS sockets as a dead-man's switch to guarantee workers are killed if your app crashes.
- **Data Plane** (`network.data_bind`): Reverse-proxies heavy HTTP/WebSocket traffic with zero overhead.

### Why It's Safe: The Dead-Man's Switch
On Linux, Herd leverages `Pdeathsig` to ensure that even if the Herd daemon itself is `kill -9`'d, the Linux kernel will instantly reap every child worker process. You never have to worry about orphaned browsers or lingering processes eating up host memory.

## 🚀 Key Features

- **Session Affinity**: Guaranteed routing of a session ID to its unique pinned worker.
- **Auto-Scaling**: Dynamically scale workers between `min` and `max` bounds based on demand.
- **Idle Eviction (TTL)**: Automatically reclaim workers that haven't been accessed within a configurable TTL.
- **Health Monitoring**: Continuous liveness checks on every worker process; dead workers are automatically replaced.
- **Singleflight Acquisition**: Protects against "thundering herd" issues where multiple concurrent requests for a new session ID try to spawn workers simultaneously.

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
