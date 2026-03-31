# Welcome to Herd: The Application Delivery Plane

**Herd** is a high-performance **Application Delivery Plane** designed to bridge the gap between low-level microVM hardware and the modern application developer experience. 

Built on **Firecracker**, Herd automates the process of translating standard Docker/OCI images into isolated, session-affine compute environments, complete with built-in L7 ingress and zero-config networking.

---

## ⚡ The Four Delivery Layers

Herd's architecture is built on four critical operational layers that guarantee speed, security, and ease of use:

1.  **OCI Translation**: Pulls images via `containerd`, extracts execution metadata (`CMD`, `ENV`), and automatically provisions Copy-on-Write rootfs snapshots.
2.  **L7 "Wake-on-Request" Proxy**: A high-speed reverse proxy that intercepts HTTP requests, cold-boots the corresponding microVM in sub-500ms (if needed), and tunnels traffic securely into the guest.
3.  **Automated IPAM**: A zero-config networking engine that manages PTP TAP interfaces, deterministic internal IP allocation, and host-side NAT routing.
4.  **Guest Agent Execution**: Injects a meticulously crafted, static `herd-guest-agent` as PID 1, which bootstraps internal microVM networking and manages the user workload execution.

---

## 🚀 Navigation

Get started with Herd today:

- [**Getting Started**](./getting-started.md): Installation and your first deployment.
- [**Architecture Deep Dive**](./architecture.md): Understanding the magic beneath the hood.
- [**CLI Reference**](./cli.md): Mastering the `herd` command suite.
- [**Configuration**](./configuration.md): Deep dive into `herd.yaml`.
