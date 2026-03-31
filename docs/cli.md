# CLI Reference

The Herd CLI (`herd`) is the primary interface for managing the daemon and interacting with microVM sessions.

---

## 🛠️ Infrastructure Commands

### `herd init`
Interactive setup for your host environment.
- **Usage**: `sudo herd init`
- **Actions**:
    - Prompts for resource limits (CPU, Memory).
    - Downloads `firecracker` and `herd-guest-agent`.
    - Bootstraps `devmapper` storage and host NAT networking.
    - Generates `herd.yaml`.

### `herd teardown`
Completely removes Herd state from the host.
- **Usage**: `sudo herd teardown`
- **Actions**:
    - Stops the `containerd` process.
    - Removes `devmapper` thin-pools and detaches loop devices.
    - Cleans up state directories.

---

## 🛰️ Daemon Commands

### `herd start`
Launches the Herd daemon.
- **Usage**: `sudo herd start [--config path/to/herd.yaml]`
- **Actions**:
    - Starts the Data Plane (Proxy) on port 8080.
    - Starts the Control Plane (REST API) on port 8081.
    - Initializes the session reaper for automatic cleanup.

---

## 🏗️ Session Commands

### `herd deploy`
Quickly spawn a new microVM session.
- **Usage**: `herd deploy --image <image> [--cmd <cmd>] [-e KEY=VALUE]`
- **Flags**:
    - `--image`: OCI image (default: `alpine:latest`).
    - `--cmd`: Command to run (comma-separated).
    - `-e`, `--env`: Environment variables.
    - `--timeout`: Idle timeout in seconds (default: `300`).

### `herd exec`
Open an interactive shell inside a running microVM.
- **Usage**: `herd exec <session-id>`
- **Note**: Connects via vsock port 5001.

### `herd logs`
Stream logs from a specific microVM session.
- **Usage**: `herd logs <session-id>`

### `herd status`
List all active microVM sessions and their current state.
- **Usage**: `herd status`

### `herd stop`
Shut down a specific microVM session and release resources.
- **Usage**: `herd stop <session-id>`
