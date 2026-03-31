# Configuration Reference

Herd is configured via a YAML file, typically located at `~/.herd/herd.yaml` or `/etc/herd/herd.yaml`.

---

## 🛰️ `network`
Controls the binding addresses for the Control and Data planes.

| Key | Description | Default |
| :--- | :--- | :--- |
| `control_bind` | REST API address for management. | `127.0.0.1:8081` |
| `data_bind` | HTTP Proxy address for traffic. | `127.0.0.1:8080` |

---

## 📦 `storage`
Configures the OCI storage backend.

| Key | Description | Default |
| :--- | :--- | :--- |
| `state_dir` | Directory for sockets and persistent state. | `~/.herd/state` |
| `namespace` | Containerd namespace for image isolation. | `herd` |
| `snapshotter_name`| Snapshot backend (usually `devmapper`). | `devmapper` |

---

## 🏗️ `resources`
Defines the global limits for the Herd instance.

| Key | Description | Default (Estimated) |
| :--- | :--- | :--- |
| `max_global_vms` | Max concurrent microVMs across all sessions. | `Cores * 4` |
| `max_global_memory_mb` | Total memory limit for all microVMs. | `80% Host RAM` |
| `cpu_limit_cores` | Total CPU capacity for all microVMs. | `Total Cores - 1` |

---

## 🤖 `binaries`
Paths to critical execution components.

| Key | Description |
| :--- | :--- |
| `firecracker_path` | Absolute path to the Firecracker binary. |
| `kernel_image_path` | Absolute path to the Linux kernel image. |
| `guest_agent_path` | Absolute path to the `herd-guest-agent` binary. |

---

## 📈 `telemetry`
Configuration for logging and metrics.

| Key | Description | Default |
| :--- | :--- | :--- |
| `log_format` | Log output format (`json` or `text`). | `json` |
| `metrics_path` | Endpoint for Prometheus metrics. | `/metrics` |
