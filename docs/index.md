# What is Herd?

> "Kubernetes is too slow to spawn sessions. Redis-only maps are too complex to maintain."

**Herd** is a session-affine process pool for Go. It manages a fleet of OS subprocess "workers" and routes incoming requests to the correct worker based on an arbitrary session ID.

> Build features, not infrastructure

### The Core Invariant
**1 Session ID → 1 Worker**, for the lifetime of the session.

This invariant transforms stateful binaries (like Browsers, LLMs, or REPLs) into multi-tenant services. Because a session always hits the same process, you can maintain in-memory state, KV caches, or local file systems without a complex coordination layer.

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
| Programming model | Go-native library | YAML / REST API | CLI / JS config |
| Crash + cleanup | ✅ per-session callback | ⚠️ pod restart only | ⚠️ restart only |
| Built-in HTTP proxy | ✅ `NewReverseProxy` | ❌ separate Ingress concern | ❌ |

### Existing OSS Landscape
| Project | Multi-process pool | Named session routing | Crash + cleanup | License | Language |
|---|---|---|---|---|---|
| Browserless | ✅ | ❌ WebSocket-sticky | ✅ | SSPL | TypeScript |
| puppeteer-cluster | ✅ | ❌ stateless tasks | ✅ | MIT | TypeScript |
| PM2 / Supervisord | ✅ | ❌ none | ⚠️ | MIT/BSD | Python/JS |
| Selenium Grid | ✅ | ✅ WebDriver-specific | ✅ | Apache 2.0 | Java |
| E2B infra | ✅ (VMs) | ✅ | ✅ | Apache 2.0 | Go (cloud-only) |
| **herd** | ✅ | ✅ explicit ID routing | ✅ | **MIT** | **Go** |
