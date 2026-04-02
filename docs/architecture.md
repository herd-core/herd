# Architecture: The Four Layers

Herd simplifies the complexity of microVM orchestration by implementing a four-layer delivery stack. This design allows standard application developers to leverage the security and isolation of Firecracker without managing raw disk images or network configurations.

---

## 📦 Layer 1: OCI Translation

Herd treats standard Docker/OCI images as first-class citizens. When you deploy an image, Herd performs the following:
- **Metadata Extraction**: It parses the image manifest for `CMD`, `WORKDIR`, and `ENV` variables.
- **Snapshotting**: Using `containerd` and the `devmapper` snapshotter, it creates a Copy-on-Write (CoW) block device from the image layers.
- **Thin-Pool Management**: Host storage is managed via a dedicated `devmapper` thin-pool, ensuring microVMs share image data while having isolated write layers.

## ⚡ Layer 2: L7 "Wake-on-Request" Proxy

The Herd Data Plane is a high-speed reverse proxy that manages session-affine routing.
- **Traffic Interception**: Requests arriving on the Data Plane (port 8080) are inspected for an `X-Session-ID` header.
- **Cold Booting**: If a session ID is provided but the corresponding microVM is not running, the proxy triggers a cold-boot sequence (sub-500ms) before forwarding the traffic.
- **Connectivity**: Once the VM is ready, traffic is tunneled through the hypervisor boundary into the guest.

## 🌐 Layer 3: Automated IPAM

Networking is isolated at the host level to prevent neighbor scanning and provide absolute containment.
- **TAP Interfaces**: Each microVM gets its own unique TAP device linked to a host-side bridge.
- **Point-to-Point Networking**: VMs are configured with a `/32` IP address, routing all traffic through a virtual gateway on the host.
- **TAP Ownership**: To maintain privilege separation, Herd explicitly assigns the TAP device's ownership to the unprivileged jailer user during creation, allowing the jailed Firecracker process to attach to it without `CAP_NET_ADMIN`.
- **NAT Routing**: Herd automatically manages `iptables` NAT (MASQUERADE) and FORWARD rules to allow internet access while preventing VM-to-VM traffic.

## 🤖 Layer 4: Guest Agent Execution

The `herd-guest-agent` is the bridge between the host daemon and the user workload.
- **Initialize RAM Disk (initrd)**: Herd injects the agent as an `initrd` ramdisk at boot time.
- **PID 1 Role**: The agent runs as `PID 1` inside the VM, responsible for mounting virtual filesystems (`/proc`, `/sys`), configuring networking, and `chroot`-ing into the user's workload filesystem.
- **Vsock Communication**: The host communicates with the agent over **AF_VSOCK**, allowing for execution commands, logs, and heartbeats without an internal network listener.

## 🔒 Layer 5: Dynamic UID Isolation

Herd implements a "Dynamic UID Pool" to ensure that untrusted guest code is strictly contained in a distinct security domain. This prevents lateral movement (cross-jail attacks) in multi-tenant environments.

- **Per-VM UID Leasing**: Every concurrent microVM is assigned a unique, numeric UID from a configured pool (e.g., `300000-301000`).
- **Cryptographic Separation**: Since each VM runs as a different UID, the Linux kernel prevents process A from signaling or interacting with process B.
- **Filesystem & Device Ownership**: Herd explicitly `chown`s the VM's chroot root, vsock sockets, and block device nodes to the leased UID.

### Directory & Device Ownership Model

| Path / Object | Creator | Owner | Mode | Purpose |
| :--- | :--- | :--- | :--- | :--- |
| `/srv/jailer/` | `herd init` | `root:root` | `0755` | Base directory for all jails. |
| `.../firecracker/<vmID>/` | `Spawn` | `root:root` | `0755` | Per-VM parent directory. |
| `.../root/` | `Spawn` | `uid_N:uid_N` | `0700` | **Chroot Root**: Only the leased UID can enter. |
| `.../root/run/` | `Spawn` | `uid_N:uid_N` | `0700` | Contains vsock sockets and config. |
| `.../root/dev/vda` | `Spawn` | `uid_N:uid_N` | `0600` | **Block Device**: Mknod node for the rootfs. |
| `tap-<suffix>` | `Spawn` | `uid_N:uid_N` | — | **Network**: TAP device owned by the jailer UID. |

> [!NOTE]
> **Numeric UIDs**: These UIDs do not require entries in `/etc/passwd`. The hypervisor and the `jailer` binary operate directly on numeric IDs for speed and to avoid host-level configuration bloat.
