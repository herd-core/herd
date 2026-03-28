# Firecracker MicroVM Execution Architecture

Herd employs a rigorous, hypervisor-backed execution model to guarantee that arbitrary user workloads run securely without sacrificing "Docker-like" transparency. 

Our architecture completely bypasses traditional SSH or HTTP orchestration agents in favor of a low-level `initrd` and `vsock` sequence.

## The Magic: `initrd` Injection
If a system boots an unmodified Docker image (like `python:latest` or `ubuntu:latest`), the Linux kernel attempts to run `/sbin/init` as `PID 1`. In standard containers, this immediately panics as no such bootloader exists.

Herd bypasses modifying user containers via **Initialize RAM Disk (initrd) injection**:
1. We package a meticulously crafted, 100% statically compiled Go binary (`herd-guest-agent`) into an `initrd.cpio` ramdisk.
2. When Firecracker boots, it loads this ramdisk straight into memory and blindly runs our agent as `PID 1`.
3. The guest agent safely ignores the container image at boot. Instead, it reaps orphaned zombie processes, mounts virtual filesystems (`/proc`, `/dev`, `/sys`), and completely bootstraps the `.guest` networking layer using `netlink`.
4. Finally, the agent mounts the *unmodified* user workload container drive (attached as `/dev/vda`) to a temporary directory and rigidly locks execution into it via a `chroot` bridge.

This grants users a strictly isolated execution substrate without *ever* forcing them to bake an agent into their provided Docker images.

## Command & Control: Vsock Dialer
Instead of running a heavy SSH daemon or proxying over a virtual HTTP network, Herd pipelines executions straight through Firecracker's **AF_VSOCK** interface.

1. **Guest Server:** The `herd-guest-agent` listens on a raw vsock port.
2. **Host Multiplexer:** Firecracker exposes this port as a Unix Domain Socket (UDS) file sitting securely on the host machine.
3. **Execution Delivery:** The `herd` host daemon dials the UDS, executes a plaintext handshake (`CONNECT 5000\n`), and streams serialized JSON execution payloads through the hypervisor boundary. Output is dynamically streamed back to the host via standard `io.Copy` buffers.

## Data Plane: Self-Contained NAT
To ensure the microVM workloads can reach the internet (e.g. `npm install`), the `herd` daemon features self-configuring networking.

When an administrator runs `sudo herd bootstrap`:
- The daemon detects the host's physical outbound networking interface.
- It automatically flips the `net.ipv4.ip_forward=1` kernel switch.
- It dynamically injects `MASQUERADE` and `FORWARD` state-tracking `iptables` constraints perfectly tailored to the `172.16.0.0/24` Firecracker subnet.

When `sudo herd teardown` is executed, it surgically uninstalls these tracking rules without disturbing UFW or Docker network constraints.
