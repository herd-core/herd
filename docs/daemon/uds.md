# Unix Domain Sockets

In the current Firecracker-based architecture, Herd uses Unix Domain Sockets (UDS) primarily for its backend dependencies rather than the public Control Plane.

## Infrastructure Sockets

### Containerd Socket (`containerd.sock`)
Herd communicates with the `containerd` daemon over a UDS. This is used for:
- Pulling and unpacking OCI images.
- Managing leases for rootfs snapshots.
- Subscribing to container events.

The path to this socket is typically `/run/containerd/containerd.sock` or elsewhere depending on your distribution, and is configured in `herd.yaml` under `storage.state_dir`.

### Firecracker Sockets
For each spawned microVM, Herd creates a unique UDS on the host to manage the Firecracker process and vsock communication:
- **API Socket**: Used for the initial Firecracker configuration (not used in `--no-api` mode).
- **Vsock Socket**: Exposes the guest-to-host vsock connection as a UDS on the host. This is how the Data Plane and `exec` commands communicate with the `herd-guest-agent` inside the VM.

## Migration Note: Control Plane
Previously, the Herd Control Plane was exposed as a gRPC service over a UDS. In the current version, the Control Plane has migrated to a **RESTful API over TCP** (defaulting to `127.0.0.1:8001`) to simplify client connectivity and observability.

For more details on the new Control Plane, see the [CLI & Configuration Reference](./cli.md).
