# Herd Dependencies

To achieve a fully automated, embedded orchestrator experience, the `herd` daemon expects a few standard low-level system binaries to be present on the host OS. 

`herd` shells out to these tools dynamically, meaning no external bash setup scripts are required. However, these binaries must exist in your `$PATH` (or be configured via absolute paths in your `herd.yaml` config).

## Required Binaries

### 1. `containerd`
**Why it's needed:** `herd` acts as a direct gRPC client to Containerd to pull OCI images and unpack them via snapshotters. `herd` will automatically generate an isolated configuration and bootstrap a private `containerd` process.
- **Provider:** Usually installed directly from [Containerd GitHub Releases](https://github.com/containerd/containerd), or via apt/yum.

### 2. `firecracker`
**Why it's needed:** The core microVM hypervisor. `herd` spawns `firecracker` instances to boot the parsed container root filesystems.
- **Provider:** [Firecracker GitHub Releases](https://github.com/firecracker-microvm/firecracker).

### 3. `dmsetup`
**Why it's needed:** The Device Mapper configuration utility. Used by `herd` to programmatically provision `thin-pool` volumes for Containerd's `devmapper` snapshotter plugin.
- **Provider:** The `dmsetup` package (often bundled in `lvm2` on Ubuntu/Debian).

### 4. `losetup`
**Why it's needed:** The Loop Device setup utility. Instead of requiring users to partition raw storage drives, `herd` creates large sparse files (metadata and data) and automatically binds them to loop devices using `losetup`. These loop devices are then fed to `dmsetup`.
- **Provider:** The `mount` package (standard on almost all Linux distributions).

---

## Example Bare-Metal Installation (Ubuntu/Debian)

```bash
# System utilities
sudo apt-get update
sudo apt-get install dmsetup mount

# Download containerd
wget https://github.com/containerd/containerd/releases/download/.../containerd-X.Y.Z-linux-amd64.tar.gz
sudo tar Cxzvf /usr/local containerd-X.Y.Z-linux-amd64.tar.gz

# Download firecracker
wget https://github.com/firecracker-microvm/firecracker/releases/download/.../firecracker-X.Y.Z-x86_64
chmod +x firecracker-X.Y.Z-x86_64
sudo mv firecracker-X.Y.Z-x86_64 /usr/local/bin/firecracker
```
