# Getting Started

This guide will walk you through installing Herd, initializing your host environment, and deploying your first microVM.

## 🛠️ Prerequisites

- **Host OS**: Linux (A recent kernel with KVM support is required).
- **Virtualization**: KVM must be available and accessible.
- **Root Access**: Most `herd` commands require `sudo`.

---

## 📦 1. System Dependencies

Before installing Herd, ensure your system has `containerd` and `iptables` installed:

```bash
sudo apt update && sudo apt install -y containerd iptables
```

---

## 📦 2. Installation

The fastest way to get Herd up and running is with our official installation script:

```bash
curl -sSL https://raw.githubusercontent.com/herd-core/herd/main/scripts/install.sh | bash
```

This script will download:
- The `herd` CLI binary.
- The `firecracker` hypervisor.
- The `herd-guest-agent` (runs inside the VMs).

---

## 🚀 2. Initialization

Before you can spawn microVMs, Herd needs to prepare your host system's storage and networking. Run the interactive `init` command:

```bash
sudo herd init
```

**What this does**:
- Sets up the `devmapper` thin-pool for high-speed rootfs snapshotting.
- Configures host-wide NAT routing for microVM internet access.
- Downloads a optimized Linux kernel (`vmlinux`) if one isn't provided.
- Generates a configuration file at `~/.herd/herd.yaml`.

---

## 🛰️ 3. Start the Daemon

Once initialized, start the Herd daemon to begin managing traffic and sessions:

```bash
sudo herd start
```

This command launches the **Control Plane** (REST API) and the **Data Plane** (HTTP Reverse Proxy).

---

## 🏗️ 4. Your First Deployment

Deploy a standard OCI image directly into a Firecracker microVM:

```bash
herd deploy --image nginx:latest
```

Herd will automatically:
1.  Pull the image via `containerd`.
2.  Create a snapshot of the root filesystem.
3.  Allocate an internal IP.
4.  Spawn the Firecracker process.
5.  Provide you with a **Session ID** and a **Proxy URL**.

You can now access your application via the proxy, which intelligently routes traffic to your dedicated microVM.
