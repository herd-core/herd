# Quickstart

Herd allows turning stateful local binaries into session-affine services quickly via a standalone daemon. Below are two common patterns: isolating headless browsers for QA/automation, and pinning users to dedicated LLM runners for memory optimization.

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
  min_workers: 1
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
sudo herd start --config ./herd.yaml
```

### 3. Usage

First, use a Herd client (which connects to the UDS Control Plane) to acquire a session. This establishes a stream that acts as a dead-man's switch. Then, connect your tools through the HTTP Data Plane proxy using the returned `session_id`.

```python
import asyncio
from playwright.async_api import async_playwright
from herd_client import HerdClient

async def main():
    # 1. Acquire session via Control Plane (UDS dead-man's switch)
    with HerdClient("/tmp/herd.sock") as client:
        session = client.acquire()

        # 2. Connect to Data Plane proxy using the allocated session ID
        async with async_playwright() as p:
            browser = await p.chromium.connect(
                "ws://127.0.0.1:8080/", 
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
  min_workers: 1
  max_workers: 10
  ttl: 10m
  worker_reuse: true
```

### 2. Running It

Start the daemon:

```bash
sudo snap install ollama
# Running without sudo will disable cgroup isolation.
sudo herd start --config ./herd.yaml
```

### 3. Usage

Just like the Playwright example, you first acquire a session over the UDS Control Plane, and then send your HTTP traffic to the Data Plane using that Session ID. Here is how that looks in Python.

```python
import requests
from herd_client import HerdClient

# 1. Acquire session via Control Plane (UDS dead-man's switch)
with HerdClient("/tmp/herd.sock") as client:
    session = client.acquire()

    # 2. Send API requests to the Data Plane proxy
    response = requests.post(
        "http://127.0.0.1:8080/api/chat",
        headers={"X-Session-ID": session.id},
        json={
            "model": "llama3",
            "messages": [{"role": "user", "content": "Hello! I am an isolated agent."}]
        }
    )
    
    print(response.json())
```
