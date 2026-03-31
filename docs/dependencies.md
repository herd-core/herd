# Herd Dependencies

This page maps host dependencies for Firecracker microVM mode, including who needs each binary and in which phase of execution.

See also:

- architecture/firecracker-storage-bootstrap.md

## Dependency Map by Phase

| Dependency | init | start | worker spawn | teardown | Purpose |
| :--- | :---: | :---: | :---: | :---: | :--- |
| dmsetup | yes | no | no | yes | Create and remove devmapper thin-pool |
| losetup | yes | no | no | yes | Attach and detach sparse-file-backed loop devices |
| blockdev | yes | no | no | no | Query loop-backed device size for dm table |
| containerd binary | no | external process | no | stopped first | Runtime gRPC API and devmapper snapshotter |
| firecracker binary | no | no | yes | no | Launch microVM process with block-device rootfs |
| pkill | no | no | no | yes | Stop isolated containerd before thin-pool removal |

## Runtime Component Graph

```mermaid
flowchart LR
	A[herd init] --> B[losetup]
	A --> C[dmsetup create herd-thinpool]
	A --> D[stateDir/config.toml]

	E[containerd --config stateDir/config.toml] --> F[devmapper snapshotter]
	F --> C

	G[herd start] --> H[containerd gRPC socket]
	H --> I[PullAndSnapshot]
	I --> J[/dev/mapper/herd-thinpool-snap-*]
	J --> K[firecracker microVM]

	L[herd teardown] --> M[pkill containerd]
	M --> N[dmsetup remove herd-thinpool]
	N --> O[losetup detach loops]
	O --> P[RemoveAll stateDir]
```

## Required Binaries

### containerd

Why needed:

- Herd uses containerd gRPC for pull, unpack, and snapshot lifecycle.
- The generated config is isolated under stateDir and points at the devmapper pool.

Provider:

- containerd release tarballs or distro packages.

### firecracker

Why needed:

- Spawned for each worker VM.
- Receives the devmapper snapshot block device as rootfs.

Provider:

- Firecracker GitHub releases.

### dmsetup

Why needed:

- Programs the thin-pool device map used by containerd devmapper snapshotter.
- Required in bootstrap and teardown.

Provider:

- Usually from lvm2 packages.

### losetup

Why needed:

- Binds sparse data and metadata files to loop devices before thin-pool creation.
- During teardown, detaches only loops associated with those exact files.

Provider:

- util-linux or distro core mount tools.

### blockdev

Why needed:

- Reads byte size of loop data device so dmsetup table length is correct.

Provider:

- util-linux package set on most Linux distributions.

## Kernel and Privilege Requirements

- Linux host with device-mapper support.
- Permissions for loop device and dmsetup operations.
- Access to run firecracker and containerd processes.

In many environments this means root or equivalent capabilities for bootstrap and teardown paths.

## Example Installation (Ubuntu or Debian)

```bash
sudo apt-get update
sudo apt-get install -y lvm2 util-linux

# containerd example install path
wget https://github.com/containerd/containerd/releases/download/.../containerd-X.Y.Z-linux-amd64.tar.gz
sudo tar Cxzvf /usr/local containerd-X.Y.Z-linux-amd64.tar.gz

# firecracker example install path
wget https://github.com/firecracker-microvm/firecracker/releases/download/.../firecracker-X.Y.Z-x86_64
chmod +x firecracker-X.Y.Z-x86_64
sudo mv firecracker-X.Y.Z-x86_64 /usr/local/bin/firecracker
```

## Preflight Checks

```bash
command -v containerd
command -v firecracker
command -v dmsetup
command -v losetup
command -v blockdev
```

If any command is missing, install that dependency before running `herd init`.
