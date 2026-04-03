# Configuration Reference

Herd is configured via a YAML file, typically located at `~/.herd/herd.yaml` or `/etc/herd/herd.yaml`.

---

## 🛰️ `network`
Controls the binding addresses for the Control and Data planes.

| Key | Description | Default |
| :--- | :--- | :--- |
| `control_bind` | REST API address for management. | `127.0.0.1:8081` |
| `data_bind` | HTTP Proxy address for traffic. | `127.0.0.1:8080` |
| `ephemeral_port_start` | Start of the random port range. | `10000` |
| `ephemeral_port_end` | End of the random port range. | `39999` |

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
| `jailer_path` | Absolute path to the Firecracker jailer binary. |
| `kernel_image_path` | Absolute path to the Linux kernel image. |
| `guest_agent_path` | Absolute path to the `herd-guest-agent` binary. |

---

## 📈 `telemetry`
Configuration for logging and metrics.

| Key | Description | Default |
| :--- | :--- | :--- |
| `log_format` | Log output format (`json` or `text`). | `json` |
| `metrics_path` | Endpoint for Prometheus metrics. | `/metrics` |

---

## 🔒 `jailer`
Parameters for the secure Firecracker jailer process.

| Key | Description | Minimum | Recommended |
| :--- | :--- | :--- | :--- |
| `uid_pool_start` | First UID (and GID) in the dynamic pool. Each concurrent MicroVM gets a unique UID from `[uid_pool_start, uid_pool_start + uid_pool_size)`. | `65536` | `300000` |
| `uid_pool_size` | Number of UIDs in the pool. Must be ≥ your `max_global_vms`. | `1` | `200` |
| `chroot_base_dir` | Root directory where per-VM jails are created (`<dir>/firecracker/<vmID>/root/`). | — | `/srv/jailer` |

> **Security model — dynamic UID isolation**
>
> Every concurrent MicroVM runs under a **unique, unprivileged UID/GID** leased from the pool. This
> ensures each VM occupies a distinct [DAC](https://en.wikipedia.org/wiki/Discretionary_access_control)
> security domain. An escaped process running as `uid_N` cannot signal, read, or reuse resources
> belonging to any other tenant's `uid_M` process.
>
> - `chroot_base_dir` is owned `root:root 0755` — traversable by anyone.
> - Each VM's `<dir>/firecracker/<vmID>/root/` is `uid_N:uid_N 0700` — traversable **only** by that VM's UID.
> - The TAP device and `/dev/vda` block node are both owned by `uid_N`, so Firecracker can access them
>   after dropping privileges to `uid_N`.
> - UIDs **do not need `/etc/passwd` entries**; the jailer uses the numeric value directly.

**Example config:**

```yaml
jailer:
  uid_pool_start: 300000
  uid_pool_size: 200
  chroot_base_dir: /srv/jailer
```

**Migration from `<= v0.5.x`:**  
Remove the old `uid` and `gid` keys; replace with `uid_pool_start` and `uid_pool_size` as shown above.
The daemon will refuse to start with the old keys due to strict YAML field validation.
