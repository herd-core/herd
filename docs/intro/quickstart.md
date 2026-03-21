# Quickstart

Herd allows turning stateful local binaries into session-affine services quickly. Below are two common patterns: isolating headless browsers for QA/automation, and pinning users to dedicated LLM runners for memory optimization.

---

## 🌐 Quick Start: Playwright Browser Isolation

Herd is perfect for creating multi-tenant browser automation gateways. In this example, each session ID gets its own dedicated Chrome instance. Because browsers maintain complex state (cookies, local storage, open pages), we configure Herd to never reuse a worker once its TTL expires, avoiding cross-tenant state leaks.

You can find the full, runnable code for this example in [`examples/playwright/main.go`](https://github.com/herd-core/herd/blob/main/examples/playwright/main.go).

### 1. The Code

```go
package main

import (
	"log"
	"net/http"
	"time"

	"github.com/hackstrix/herd"
	"github.com/hackstrix/herd/proxy"
)

func main() {
	// 1. Spawns an isolated npx playwright run-server per user
	factory := herd.NewProcessFactory("npx", "playwright", "run-server", "--port", "{{.Port}}", "--host", "127.0.0.1").
		WithHealthPath("/").
		WithStartTimeout(1 * time.Minute).
		WithStartHealthCheckDelay(500 * time.Millisecond)

	// 2. Worker reuse is disabled to prevent state leaks between sessions
	pool, _ := herd.New(factory,
		herd.WithAutoScale(1, 5), // auto-scale between 1 and 5 concurrent tenants (until expires)
		herd.WithTTL(15 * time.Minute),
		herd.WithWorkerReuse(false), // CRITICAL: Never share browsers between users
	)

	// 3. Setup proxy to intelligently route WebSocket connections
	mux := http.NewServeMux()
	mux.Handle("/", proxy.NewReverseProxy(pool, func(r *http.Request) string {
		return r.Header.Get("X-Session-ID") // Pin by X-Session-ID
	}))

	log.Fatal(http.ListenAndServe(":8080", mux))
}
```

### 2. Running It

Start the gateway (assuming you are in the `examples/playwright` directory):

```bash
sudo snap install node
npx playwright install --with-deps
# Running without sudo will disable cgroup isolation.
sudo go run .
```

### 3. Usage

Connect to the gateway using Python and Playwright. Herd guarantees that all requests with the same `X-Session-ID` connect to the exact same browser instance, preserving your state (like logins, cookies, and tabs) across reconnections as long as your session TTL hasn't expired!

```python
import asyncio
from playwright.async_api import async_playwright

async def main():
    async with async_playwright() as p:
        # Herd routes based on X-Session-ID header
        browser = await p.chromium.connect(
            "ws://127.0.0.1:8080/", 
            headers={"X-Session-ID": "my-secure-session"}
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

You can find the full, runnable code for this example in [`examples/ollama/main.go`](https://github.com/herd-core/herd/blob/main/examples/ollama/main.go).

### 1. The Code

```go
package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/hackstrix/herd"
	"github.com/hackstrix/herd/proxy"
)

func main() {
	// 1. Define how to spawn an Ollama worker on a dynamic port
	factory := herd.NewProcessFactory("ollama", "serve").
		WithEnv("OLLAMA_HOST=127.0.0.1:{{.Port}}").
		WithHealthPath("/").
		WithStartTimeout(2 * time.Minute).
		WithStartHealthCheckDelay(1 * time.Second)

	// 2. Create the pool with auto-scaling and TTL eviction
	pool, _ := herd.New(factory,
		herd.WithAutoScale(1, 10),
		herd.WithTTL(10 * time.Minute),
		herd.WithWorkerReuse(true),
	)

	// 3. Setup a session-aware reverse proxy
	mux := http.NewServeMux()
	mux.Handle("/api/", proxy.NewReverseProxy(pool, func(r *http.Request) string {
		return r.Header.Get("X-Agent-ID") // Pin worker by X-Agent-ID header
	}))

	log.Fatal(http.ListenAndServe(":8080", mux))
}
```

### 2. Running It

Start the gateway (assuming you are in the `examples/ollama` directory):

```bash
sudo snap isntall ollama
# Running without sudo will disable cgroup isolation.
sudo go run .
```

### 3. Usage

Send requests with an `X-Agent-ID` header. Herd guarantees that all requests with the same ID will hit the exact same underlying `ollama serve` instance!

```bash
curl -X POST http://localhost:8080/api/chat \
  -H "X-Agent-ID: agent-42" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "llama3",
    "messages": [{"role": "user", "content": "Hello! I am agent 42."}]
  }'
```
