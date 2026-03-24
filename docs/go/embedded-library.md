# Using Herd as a Go Library

While Herd is primarily run as a standalone daemon, you can still embed it directly into your own Go applications as a powerful process manager and reverse proxy.

## Installation

```bash
go get github.com/herd-core/herd
```

## Example: Playwright Browser Isolation

Herd is perfect for creating multi-tenant browser automation gateways. In this example, each session ID gets its own dedicated Chrome instance. Because browsers maintain complex state (cookies, local storage, open pages), we configure Herd to never reuse a worker once its TTL expires, avoiding cross-tenant state leaks.

### 1. The Code

```go
package main

import (
	"log"
	"net/http"
	"time"

	"github.com/herd-core/herd"
	"github.com/herd-core/herd/proxy"
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

## Example: Ollama Multi-Agent Gateway

Here is an example of turning `ollama serve` into a multi-tenant LLM gateway where each agent (or user) gets their own dedicated Ollama process.

### 1. The Code

```go
package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/herd-core/herd"
	"github.com/herd-core/herd/proxy"
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