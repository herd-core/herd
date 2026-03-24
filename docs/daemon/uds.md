# Unix Domain Sockets

Herd uses Unix Domain Sockets (UDS) for the local control plane in single-node daemon mode.

## Why UDS

- Keeps control traffic local to the host.
- Avoids exposing control APIs over TCP.
- Simplifies local SDK connectivity and access controls.

## Socket Path

Configured via:

```yaml
network:
	control_socket: /tmp/herd.sock
```

Validation rules:

- Must be an absolute path.
- Must fit Unix socket path length limits.

## Security and Lifecycle

At daemon startup:

1. Any stale socket file is removed.
2. Socket is re-created and bound.
3. Permissions are set to `0600`.

At daemon shutdown:

1. gRPC server stops.
2. Listener closes.
3. Socket file is removed.

## Control Stream Semantics

The control stream owns session liveness.

- If stream closes normally (`io.EOF`), session worker is force-terminated.
- If stream breaks with error, session worker is force-terminated.

This prevents leaked stateful subprocesses after local client crashes.

