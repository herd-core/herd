# Welcome to Herd: The Edge Cloud Core

**Herd** is a high-performance **Application Delivery Plane** for stateful workloads. Built on **Firecracker**, it automates the translation of standard Docker/OCI images into isolated, session-affine compute environments with built-in L7 ingress and zero-config networking.

## 🛸 From Orchestrator to Edge Cloud

Herd isn't just an "engine block"—it's the whole vehicle. It bridges the gap between raw microVM hardware and the application developer experience.

| Feature | Pure Orchestrator (e.g., Raw Firecracker) | Herd (Fly.io Open Source Core) |
| :--- | :--- | :--- |
| **Input** | Custom Kernel + Raw Disk | Standard OCI/Docker Image |
| **Ingress** | None (You install Nginx/Traefik) | **Built-in L7 Proxy** |
| **Lifecycle** | Turn On / Turn Off | **Wake-on-HTTP-Request** |
| **Effort** | Systems Engineer (Hard) | Application Developer (Easy) |

## ✅ The Core Value: The Four Layers

1.  **OCI Translation**: Pull images, extract `CMD`/`ENV`, and snap rootfs volumes automatically.
2.  **L7 Proxy Ingress**: Intercept traffic, cold-boot VMs, and tunnel TCP/HTTP connections.
3.  **Automated IPAM**: Zero-config host-to-guest networking.
4.  **Agent-Driven Execution**: Abstract the OS with a static `herd-guest-agent` as PID 1.

## 🧱 Key Documentation

- [**The Brutal Difference**](../../README.md#the-herd-difference): Why Herd isn't just another KVM wrapper.
- [**Architecture Deep Dive**](../../architecture.md): Understanding the four delivery layers.
- [**Installation Guide**](./daemon/install.md): Bootstrapping the edge cloud.
- [**CLI & Configuration Reference**](./daemon/cli.md): Mastering the `herd` binary.

## 🚀 Get Started

Ready to transform your stateful apps? Start with the [Installation Guide](./daemon/install.md).
