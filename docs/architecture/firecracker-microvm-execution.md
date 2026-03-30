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

## Command & Control: Multi-Port Vsock Dialer
Instead of running a heavy SSH daemon or proxying over a virtual HTTP network, Herd pipelines executions straight through Firecracker's **AF_VSOCK** interface.

1. **Port 5000 (Control Plane):** The `herd` host daemon dials the UDS and streams serialized JSON execution payloads through the hypervisor boundary. Output is dynamically streamed back to the host via standard `io.Copy`.
2. **Port 5001 (Exec Bridge):** Used for dynamic administration. Executing `herd exec <vm-id>` on the host dials 5001, which immediately triggers the `herd-guest-agent` to spawn `/bin/sh` inside the `chroot` with a pseudo-terminal (PTY) attached directly to the vsock.

## Data Plane: Dynamic IPAM & Zero-Waste Networking
To ensure the microVM workloads have deterministic networking with absolute containment, Herd employs a dynamic IP Address Management (IPAM) engine.

- **Point-to-Point `/32` Routing:** Unlike standard bridge networking which weakens isolation, Herd allocates a single `/32` IP from a continuous block (e.g. `10.200.0.0/16`) via `boot_args`.
- **Absolute Containment:** The host assigns each VM a unique TAP device governed by a point-to-point (`peer`) linkage to the `172.16.0.1` or `10.200.0.1` gateway. Traffic can only leave the VM by hitting the host `iptables` FORWARD rules.
- **Outbound NAT:** `MASQUERADE` iptables ensure standard internet connectivity.
- **Inbound Drops:** We drop all traffic destined for internal RFC 1918 subnets, meaning a compromised MicroVM cannot scan your local LAN or talk to other neighboring MicroVMs.

When an administrator runs `sudo herd bootstrap`:
- The daemon detects the host's physical outbound networking interface.
- It automatically flips the `net.ipv4.ip_forward=1` kernel switch.
- It dynamically injects `MASQUERADE` and `FORWARD` state-tracking `iptables` constraints perfectly tailored to the `172.16.0.0/24` Firecracker subnet.

When `sudo herd teardown` is executed, it surgically uninstalls these tracking rules without disturbing UFW or Docker network constraints.
