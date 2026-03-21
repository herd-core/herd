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

## 🏭 3. ProcessFactory (Built-in)

The default implementation is `ProcessFactory`. It manages local OS subprocesses.

### Basic Usage

```go
factory := herd.NewProcessFactory("npx", "playwright", "run-server", "--port", "{{.Port}}")
```

### ⚙️ Configuration & Sandboxing

You can chain options to configure limits and environment variables. On Linux, Herd automatically enables a **namespace & cgroup sandbox** for isolation.

| Method | Description |
| :--- | :--- |
| `WithEnv(kv string)` | Injects an environment variable. Supports `{{.Port}}` expansion. |
| `WithHealthPath(path)` | Sets the HTTP endpoint polled to decide readiness (default `/health`). |
| `WithStartTimeout(d)` | Maximum time to wait for first healthy response. |
| `WithMemoryLimit(bytes)` | Sets the cgroup `memory.max` limit for the worker (Linux). |
| `WithCPULimit(cores)` | Sets the cgroup CPU quota (e.g., `0.5` for half a core) (Linux). |
| `WithPIDsLimit(n)` | Sets maximum number of PIDs in the cgroup (Linux). |
| `WithInsecureSandbox()` | Disables the namespace/cgroup sandbox. *Not recommended for production.* |

> **⚠️ Linux Sandboxing**: Setting cgroup limits requires running with root privileges (or a delegated slice) on a system with cgroup v2 enabled to enforce absolute Isolation between independent sessions.
