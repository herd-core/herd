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

## 🔒 Layer 5: Secure Jailer Isolation

Herd implements the "Jailer Pattern" to ensure that untrusted guest code is strictly contained even if the hypervisor itself is compromised.
- **Privilege Dropping**: The hypervisor process drops all root privileges and runs as a dedicated, unprivileged `firecracker` user (UID 900).
- **Filesystem Isolation**: Each microVM is locked into a private chroot environment with `0700` permissions. This ensures the VM's filesystem is only accessible to the jailer process and root, protecting it from unauthorized host-level inspection by other users or processes.
- **Resource Cgroups**: The jailer automatically places the microVM process into dedicated `cpu` and `memory` cgroups for hard multi-tenant resource budgeting.
