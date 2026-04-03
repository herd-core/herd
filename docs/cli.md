# CLI Reference

The Herd CLI (`herd`) is the primary interface for managing the daemon and interacting with microVM sessions.

---

## 🛠️ Infrastructure Commands

### `herd init`
Initialize herd on your host environment.
- **Usage**: `sudo herd init [flags]`
- **Flags**:
    - `-y, --yes`: Skip interactive setup and use defaults/flags.
    - `--firecracker-path <path>`: Custom path to Firecracker binary.
    - `--jailer-path <path>`: Custom path to jailer binary.
    - `--kernel-path <path>`: Custom path to Linux kernel image.
    - `--chroot-base-dir <path>`: Jailer chroot base directory (default: `/srv/jailer`).
    - `--max-vms <int>`: Max global concurrent MicroVMs.
    - `--max-memory <int>`: Max global memory in MB.
    - `--cpu-cores <float>`: CPU limit in cores.
- **Actions**:
    - Prompts for resource limits (CPU, Memory) if not in non-interactive mode.
    - Downloads `firecracker`, `jailer`, and `herd-guest-agent` if not found.
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
- **Usage**: `sudo herd start [--config path/to/herd.yaml] [--interface eth0]`
- **Flags**:
    - `--interface <name>`: Public network interface to report to the control plane (e.g., `eth0`).
- **Actions**:
    - Determines the node's public IP from the specified interface.
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
    - `-p`, `--publish`: Publish a guest port to the host.
        - Format: `[host_interface:]host_port:guest_port[/protocol]`
        - Examples:
            - `8080:80` (Bind all interfaces)
            - `127.0.0.1:8080:80` (Localhost only)
            - `:80` (Random host port allocation)
            - `53:53/udp` (UDP mapping)

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
