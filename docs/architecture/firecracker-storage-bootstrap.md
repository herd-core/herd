# Firecracker Storage Bootstrap

This page documents how Herd prepares host storage for Firecracker microVM workers that use containerd plus the devmapper snapshotter.

## Scope

This flow is for daemon mode where worker root filesystems are backed by device-mapper snapshots and attached as block devices to Firecracker VMs.

## High-Level Lifecycle

1. Run `herd init`.
2. `herd init` automatically starts `containerd` with the generated config (or guides you to).
3. Run `herd start`.
4. Workers pull and snapshot OCI images.
5. Firecracker boots with the extracted block device.
6. Run `herd teardown` when done.

## Bootstrap Internals

Implementation reference: internal/storage/bootstrap.go.

Bootstrap uses a rollback-safe cleanup stack so partial failures do not leave orphaned kernel state.

### Rollback Model

- Bootstrap has a named error return.
- Every successful step pushes a cleanup function.
- A deferred block runs cleanup in reverse order if any step fails.

### Step Order

1. Create state directories:
- stateDir
- stateDir/devmapper

2. Create sparse files:
- stateDir/devmapper/data (2 GiB)
- stateDir/devmapper/metadata (1 GiB)

3. Bind loop devices:
- losetup --find --show on data file
- losetup --find --show on metadata file

4. Create thin pool:
- dmsetup create herd-thinpool --table ...

5. Generate containerd config:
- stateDir/config.toml
- stateDir/containerd directory
- GRPC socket at stateDir/containerd.sock
- devmapper snapshotter config points to herd-thinpool

### What Gets Rolled Back

If bootstrap fails, cleanup runs in strict LIFO order and can include:

1. Remove generated config.toml.
2. Remove containerd directory if created by bootstrap.
3. Remove herd-thinpool if created by bootstrap.
4. Detach loop devices that bootstrap attached.
5. Remove sparse files that bootstrap created.
6. Remove created directories if they did not exist before.

## Teardown Internals

Implementation reference: internal/storage/bootstrap.go, Teardown function.

Teardown intentionally destroys bootstrapped storage state in dependency-safe order.

### Ordered Teardown Sequence

1. Stop isolated containerd:
- pkill -f "containerd --config <stateDir>/config.toml"

2. Wait for resource release:
- sleep 1 second to reduce Device or resource busy races.

3. Remove thin pool with retries:
- dmsetup remove herd-thinpool
- retries are used if dependencies release slowly.

4. Detach only matching loop devices:
- losetup -j <stateDir>/devmapper/data
- losetup -j <stateDir>/devmapper/metadata
- losetup -d only returned loop devices

5. Purge state directory:
- rm -rf <stateDir>

## Runtime Storage Path During Spawn

Implementation reference: internal/storage/containerd.go and firecracker_factory.go.

1. Manager.PullAndSnapshot pulls and unpacks image via containerd.
2. SnapshotService.Prepare creates a devmapper snapshot for the VM.
3. Herd extracts the block-device path from mount source or mount options.
4. Firecracker is launched with the extracted block device as rootfs.

## Required Operator Commands

Example using test configuration values:

```bash
sudo herd init
sudo herd start
```

Explicit teardown:

```bash
sudo herd teardown
```

## Common Failure Modes

### 1. containerd socket timeout

Symptom:
- failed to dial .../containerd.sock

Cause:
- containerd was not started with generated config.toml.

Fix:
- start containerd using stateDir/config.toml.

### 2. firecracker executable not found

Symptom:
- exec: firecracker: executable file not found in PATH

Cause:
- firecracker binary is not installed or not visible in runtime PATH.

Fix:
- install firecracker and ensure herd can execute it.

### 3. thin-pool busy on teardown

Symptom:
- dmsetup remove fails with busy resource errors.

Cause:
- containerd still holds references to pool-backed snapshots.

Fix:
- stop containerd first and retry remove.

## Operational Notes

- Keep stateDir isolated per environment to avoid accidental shared kernel objects.
- Run bootstrap and teardown with privileges required for dmsetup and loop device operations.
- Prefer target_idle: 1 while validating first boot path to reduce parallel image pull contention.
