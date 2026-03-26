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

## 🌐 Quick Start: Playwright Browser Isolation

Herd is perfect for creating multi-tenant browser automation gateways. In this example, each session ID gets its own dedicated Chrome instance managed by the Herd Daemon. Because browsers maintain complex state (cookies, local storage, open pages), we configure Herd to never reuse a worker once its TTL expires, avoiding cross-tenant state leaks.

### 1. The Configuration (`herd.yaml`)

```yaml
network:
  data_bind: 127.0.0.1:8080

worker:
  command: ["npx", "playwright", "run-server", "--port", "{{.Port}}", "--host", "127.0.0.1"]

resources:
  target_idle: 1
  max_workers: 5
  ttl: 15m
  worker_reuse: false # CRITICAL: Never share browsers between users
```

### 2. Running It

Install Playwright dependencies, and then start the Herd daemon:

```bash
sudo snap install node
npx playwright install --with-deps
# Running without sudo will disable cgroup isolation.
sudo ./herd start --config ./herd.yaml
```

### 3. Usage

First, explicitly install the `herd-client` package to communicate with the daemon:

```bash
pip install herd-client
```

Next, use a Herd client (which connects to the UDS Control Plane) to acquire a session. This establishes a stream that acts as a dead-man's switch. Then, connect your tools through the HTTP Data Plane proxy using the allocated session ID and proxy URL.

```python
import asyncio
from playwright.async_api import async_playwright
from herd import AsyncClient

async def main():
    # 1. Connect to the Herd daemon via the Control Plane socket
    client = AsyncClient("unix:///tmp/herd.sock")
    
    # 2. Acquire a session (holds a dead-man's switch via heartbeat)
    async with client.acquire(worker_type="playwright-worker") as session:
        print(f"Acquired worker session: {session.id}")

        # 3. Connect to the Data Plane proxy using the exact assigned session proxy_url
        async with async_playwright() as p:
            # We pass the X-Session-ID header so the Proxy maps us to the correct worker
            browser = await p.chromium.connect(
                f"ws://127.0.0.1:8080/", 
                headers={"X-Session-ID": session.id}
            )
            
            ctx = await browser.new_context()
            page = await ctx.new_page()
            await page.goto("https://github.com")
            print(await page.title())
            await browser.close()

asyncio.run(main())
```

---

## 🛠️ Quick Start: Ollama Multi-Agent Gateway

Here is an example of turning `ollama serve` into a multi-tenant LLM gateway where each agent (or user) gets their own dedicated Ollama process. This is specifically useful for isolating context windows or KV caches per agent without downloading models multiple times.

### 1. The Configuration (`herd.yaml`)

```yaml
network:
  data_bind: 127.0.0.1:8080

worker:
  command: ["ollama", "serve"]
  env:
    - "OLLAMA_HOST=127.0.0.1:{{.Port}}"

resources:
  target_idle: 1
  max_workers: 10
  ttl: 10m
  worker_reuse: true
```

### 2. Running It

Start the daemon:

```bash
sudo snap install ollama
# Running without sudo will disable cgroup isolation.
sudo ./herd start --config ./herd.yaml
```

### 3. Usage

Just like the Playwright example, you first install `herd-client`. We'll use the easy-to-read synchronous API for this example so you can easily drop it into standard scripts.

```bash
pip install herd-client
```

```python
import requests
from herd import Client

# 1. Connect to the Herd daemon via the Control Plane socket
client = Client("unix:///tmp/herd.sock")

# 2. Acquire session via Control Plane (context manager handles background heartbeats)
with client.acquire(worker_type="ollama") as session:
    print(f"Acquired worker session: {session.id}")

    # 3. Send API requests directly to this worker via the Session Proxy URL
    response = requests.post(
        f"{session.proxy_url}/api/chat",
        headers={"X-Session-ID": session.id},
        json={
            "model": "llama3",
            "messages": [{"role": "user", "content": "Hello! I am an isolated agent."}]
        }
    )
    
    print(response.json())
```

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
