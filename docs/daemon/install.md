# Installation (Daemon)

The Herd daemon runs as a standalone binary and is configured via a strict YAML file. It requires root privileges to manage microVMs, networking, and storage.

See also:
- [Host Dependencies](../dependencies.md)

## Install

The easiest way to install Herd is via the official installation script:

```bash
curl -sSL https://raw.githubusercontent.com/herd-core/herd/main/scripts/install.sh | bash
```

This will download the `herd` binary, `firecracker`, and the `herd-guest-agent` into your environment.

## Initialize

Before running the daemon, you must initialize the host environment (storage, networking, and kernel).

```bash
sudo herd init
```

## Run

```bash
sudo herd start
```

## Why does Herd require root (`sudo`)?

Herd requires elevated privileges to leverage hardware-level virtualization and advanced networking:

- **Block Storage (`CAP_SYS_ADMIN`)**: To provision root filesystems, Herd creates devmapper thin-pool snapshots and mounts them.
- **Networking (`CAP_NET_ADMIN`)**: Creating TAP interfaces, binding them to bridges, and configuring NAT routing requires networking privileges.
- **Containerd**: Interaction with the `containerd.sock` often requires root access by default.

## Platform Guarantees

- **Linux**: Full support for Firecracker, KVM, and high-performance networking.
- **macOS/Other**: Not supported for Firecracker mode (requires KVM).
