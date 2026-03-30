## Plan: Containerd Storage Pipeline

Integrate `github.com/containerd/containerd` using the devmapper snapshotter to pull OCI images, unpack them into block devices, and attach them to Firecracker microVMs. To maintain a frictionless out-of-the-box experience, `herd` will auto-bootstrap its own storage requirements dynamically on startup without requiring external provisioning scripts.

**Steps**
1. **Self-Bootstrapping Storage (Go-native):** Create `internal/storage/bootstrap.go` to handle fully autonomous devmapper setup during daemon start.
    * Automatically create loopback-backed sparse files (data and metadata) in the `herd` state directory (e.g., `/var/lib/herd/devmapper`).
    * Automatically bind them to loop devices using `losetup` via `exec.Command`.
    * Provision the `devmapper` thin-pool dynamically using `dmsetup`.
    * Generate a sandboxed `containerd` configuration (`config.toml`) programmatically.
2. **Dependencies:** Add `github.com/containerd/containerd` and its related protobuf dependencies to `go.mod`.
3. **Configuration:** Add a `StorageConfig` struct to `internal/config/config.go` containing `StateDir`, `Namespace`, and `SnapshotterName` (default to "devmapper"). Expose these in `testdata/herd.yaml`.
4. **Embedded Containerd lifecycle (Optional/Recommended):** Either have the `herd` daemon spawn an isolated `containerd` process as a child process with the auto-generated config, or connect to an existing system `containerd`. If spawning a child process, manage its lifecycle in `cmd/herd/start.go`.
5. **Storage Manager (Core Phase):** Create `internal/storage/containerd.go` to implement the containerd client wrapper:
    * *Initialization:* Connect to the gRPC socket.
    * *Leasing & Pulling:* Implement `PullImage(ctx, ref, vmID)` to pull an OCI image and create a lease tied to the `vmID` to prevent GC.
    * *Snapshotting & Extraction:* Implement `CreateSnapshot(ctx, image, vmID)` that calls `image.Unpack(ctx, "devmapper")`, queries the snapshotter via `snapService.Mounts()`, and returns the `mountPoint.Source` (e.g., `/dev/mapper/snap-123`).
    * *Cleanup:* Implement `ReleaseSnapshot(ctx, vmID)` to delete the snapshot and the associated lease.
6. **Factory Integration:** Modify `firecracker_factory.go`:
    * Inject the `storage.Manager` into `FirecrackerFactory`.
    * In `Spawn()`, invoke `PullImage` and `CreateSnapshot` to dynamically acquire a block device path.
    * Update the Firecracker spawn logic to map this dynamic block device as the VM's `/dev/vda`.
7. **Teardown Integration:** Update `FirecrackerWorker.Close()` to call `storage.ReleaseSnapshot(ctx, vmID)` ensuring the block device is cleaned up when the worker dies.

**Relevant files**
- `internal/storage/bootstrap.go` — Go-native devmapper and containerd bootstrapping.
- `internal/config/config.go` — Add `StateDir`, `Namespace`, and `Snapshotter` fields.
- `internal/storage/containerd.go` — The core logic for `PullImage`, `CreateSnapshot`, and `ReleaseSnapshot`.
- `firecracker_factory.go` — Update `FirecrackerFactory` to consume the storage manager.
- `cmd/herd/start.go` — Wire up the containerd client and pass it to the factory.

**Verification**
1. Start the `herd` daemon on a fresh machine (with only `dmsetup` and `containerd` binaries installed).
2. Daemon automatically provisions `/var/lib/herd` loopback files and thin-pools without user intervention.
3. Submit a request to run an image (e.g., `ubuntu:latest`).
4. Wait for the VM to start and run `sudo dmsetup ls` to verify the thin-provisioned devmapper block device is active.
5. Stop the VM and verify the `devmapper` entry is successfully torn down.

**Decisions**
- Use containerd `leases` tied to the microVM UUID to prevent containerd's garbage collector from deleting the image layers while the microVM is actively running.
- Use a dedicated containerd socket and auto-generated `config.toml` to completely avoid interference with the host machine's regular Docker/Containerd workloads.
- Trust containerd's `mount.Source` exactly as the raw block device path.
