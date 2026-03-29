# Using Herd as a Go Library

While Herd is primarily run as a standalone daemon, you can still embed it directly into your own Go applications as a powerful process manager and reverse proxy.

## Installation

```bash
go get github.com/herd-core/herd
```

## Generic Example

Herd is perfect for creating multi-tenant environments. In this example, each session ID gets its own dedicated worker.

### The Code

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
	// 1. Definte a factory that spawns workers
	factory := &herd.FirecrackerFactory{
		FirecrackerPath: "/usr/bin/firecracker",
		KernelImagePath: "/opt/herd/vmlinux.bin",
		InitrdPath:      "/opt/herd/initrd.img",
		SocketPathDir:   "/tmp/herd-vms",
	}

	// 2. Control worker lifecycle (TTL eviction, auto-scale, max concurrency)
	pool, _ := herd.New(factory,
		herd.WithAutoScale(1, 5),
		herd.WithTTL(15 * time.Minute),
		herd.WithWorkerReuse(false), // Disable reuse to prevent state leaks
	)

	// 3. Setup proxy to intelligently route traffic
	mux := http.NewServeMux()
	mux.Handle("/", proxy.NewReverseProxy(pool, func(r *http.Request) string {
		return r.Header.Get("X-Session-ID") // Pin by X-Session-ID
	}))

	log.Fatal(http.ListenAndServe(":8080", mux))
}
```