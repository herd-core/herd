# CLI Command Reference

The Herd binary (`herd`) provides several commands for managing the lifecycle of the daemon, its storage, and microVM sessions.

## 🛠️ Infrastructure Commands

These commands are used to prepare the host environment before starting the daemon.

### `herd bootstrap`
Initializes the host system requirements for Firecracker microVMs.
- **Purpose**: Creates sparse files for storage, binds them to loop devices, sets up the devmapper thin-pool, and configures `containerd` to use the thin-pool. It also initializes host NAT routing.
- **Flags**:
    - `--config`: Path to the YAML configuration file (default: `/etc/herd/config.yaml`).
- **Usage**:
  ```bash
  sudo ./herd bootstrap --config herd.yaml
  ```

### `herd teardown`
Cleans up the host environment and removes all state.
- **Purpose**: Stops the isolated `containerd` process, removes devmapper thin-pools, detaches loop devices, and deletes state directory artifacts.
- **Flags**:
    - `--config`: Path to the YAML configuration file (default: `/etc/herd/config.yaml`).
- **Usage**:
  ```bash
  sudo ./herd teardown --config herd.yaml
  ```

---

## 🛰️ Daemon Commands

### `herd start`
Starts the Herd daemon, exposing the Control and Data planes.
- **Purpose**: Initializes the worker pool, connects to `containerd`, and starts the REST Control Plane and HTTP Data Plane.
- **Flags**:
    - `--config`: Path to the YAML configuration file (default: `/etc/herd/config.yaml`).
- **Usage**:
  ```bash
  sudo ./herd start --config herd.yaml
  ```

---

## 🏗️ Session & Worker Commands

These commands interact with a running Herd daemon.

### `herd deploy`
Spawns a new microVM session via the Control Plane REST API.
- **Purpose**: A CLI helper to quickly acquire a session without writing a custom client.
- **Flags**:
    - `--image`: The OCI image to deploy (default: `alpine:latest`).
    - `--cmd`: Command to run inside the VM (comma-separated, e.g., `--cmd=/bin/sh,-c,"echo hello"`).
    - `-e`, `--env`: Set environment variables (e.g., `-e KEY=VALUE`).
    - `--timeout`: Idle timeout in seconds (default: `300`).
    - `--config`: Path to the YAML config (used to find the `ControlBind` address).
- **Usage**:
  ```bash
  ./herd deploy --image playwright/node --cmd=npx,playwright,run-server
  ```

### `herd exec [vm-id]`
Drops you into an interactive shell inside a running microVM.
- **Purpose**: Connects to the VM's internal vsock port 5001. Requires the VM ID (which is the Session ID).
- **Usage**:
  ```bash
  ./herd exec sess-12345
  ```

### `herd logs [session-id]`
Streams real-time logs from a specific microVM session.
- **Purpose**: Fetches logs from the daemon's REST API and streams them to your terminal.
- **Flags**:
    - `--config`: Path to the YAML config (used to find the `ControlBind` address).
- **Usage**:
  ```bash
  ./herd logs sess-12345
  ```

---

## ⚙️ Configuration Reference (`herd.yaml`)

| Section | Key | Description |
| :--- | :--- | :--- |
| **network** | `control_bind` | REST API address (e.g., `127.0.0.1:8001`). |
| | `data_bind` | Proxy address (e.g., `127.0.0.1:8080`). |
| **storage** | `state_dir` | Directory for daemon state and `containerd` socket. |
| | `namespace` | `containerd` namespace for isolation. |
| | `snapshotter_name` | `containerd` snapshotter (e.g., `devmapper`). |
| **resources** | `max_global_vms` | Max concurrent microVMs. |
| | `max_global_memory_mb` | Total memory limit for the fleet. |
| | `cpu_limit_cores` | Total CPU cores limit. |
| **telemetry** | `log_format` | Daemon log format (`json` or `text`). |
| | `metrics_path` | Endpoint for Prometheus metrics (e.g., `/metrics`). |
