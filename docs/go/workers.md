# Worker Interfaces & Factories

Herd is built around two core interfaces defined in [`worker.go`](https://github.com/herd-core/herd/blob/main/worker.go) that manage the lifecycle and communication with stateful subprocesses.

---

## 🔧 1. Worker[C]

The `Worker[C]` interface represents a single running subprocess managed by the pool.

`C` is the typed client that your application uses to talk to the process (e.g., `*http.Client`, a gRPC client stub, or a custom struct).

```go
type Worker[C any] interface {
    // ID returns a stable, unique identifier for this worker (e.g., "worker-3").
    ID() string

    // Address returns the internal network URI the worker is listening on.
    Address() string

    // Client returns the typed connection to the worker process.
    Client() C

    // Healthy performs a liveness check against the subprocess.
    // Returns nil if the worker is accepting requests.
    Healthy(ctx context.Context) error

    // OnCrash sets a callback triggered when the worker process exits unexpectedly.
    OnCrash(func(sessionID string))

    // Close performs graceful shutdown of the worker process.
    io.Closer
}
```

---

## 🏗️ 2. WorkerFactory[C]

The `WorkerFactory[C]` knows how to spawn a process and return a ready-to-use `Worker[C]`.

```go
type WorkerFactory[C any] interface {
    // Spawn starts one new worker and blocks until it is healthy.
    Spawn(ctx context.Context) (Worker[C], error)
}
```

Most users will **not** need to implement this interface directly. They will instead use the built-in `ProcessFactory`.

---

## 🏭 3. FirecrackerFactory (Built-in)

The default implementation is `FirecrackerFactory`. It manages fully isolated microVMs via Firecracker.

### Basic Usage

```go
factory := &herd.FirecrackerFactory{
    FirecrackerPath: "/usr/bin/firecracker",
    KernelImagePath: "/opt/herd/vmlinux.bin",
    InitrdPath:      "/opt/herd/initrd.img",
    SocketPathDir:   "/tmp/herd-vms",
}
```

### ⚙️ MicroVM Sandboxing

Herd inherently uses hardware-level virtualization through Firecracker, meaning complete network, CPU, and memory isolation without relying on OS-level cgroups or namespaces.
