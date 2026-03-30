# Welcome to Herd

**Herd** is a high-performance microVM orchestrator designed for stateful workloads. It leverages **Firecracker** to spawn isolated, session-affine compute environments in milliseconds, transforming stateful binaries (browsers, LLMs, REPLs) into secure, multi-tenant services.

## Why Herd?

> "Kubernetes is too slow to spawn sessions. Redis-only maps are too complex to maintain. Containers aren't isolated enough."

Herd provides:
- ⚡ **Sub-500ms startup**: Faster than any container orchestrator.
- 🔒 **Hardware Isolation**: Strong security via microVMs.
- 🔌 **REST API**: Simple Control Plane for session lifecycle, exec, and logs.

### The Core Invariant
**1 Session ID → 1 Worker (MicroVM)**, for the lifetime of the session.

This invariant enables you to maintain in-memory state, local file systems, or GPU context without a complex coordination layer.

## 🧱 Architecture Overview

Herd operates on a dual-plane architecture:

- **Control Plane** (REST API): Manages session lifecycles, image warming, and log retrieval.
- **Data Plane** (HTTP Proxy): High-speed reverse proxy that routes traffic to VMs based on `X-Session-ID`.

For a deep dive, see the [Architecture Documentation](../../architecture.md).

## 🚀 Key Features

- **Hardware Isolation**: Each session runs in its own Firecracker microVM.
- **Auto-Scaling**: Dynamically scale your VM fleet based on demand.
- **Idle Eviction**: Automatically reclaim resources for inactive sessions.
- **Exec & Logs**: Built-in support for executing commands inside VMs and retrieving real-time logs.
- **Container-Native**: Uses `containerd` and OCI images for rootfs provisioning.

## 🛠️ Get Started

Ready to dive in? Follow the [Installation Guide](./daemon/install.md) to set up your Herd daemon.
