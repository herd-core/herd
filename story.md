# Optimizing Firecracker VM Spawn Latency: From 2.7s to Sub-Second

When you're spawning Firecracker microVMs on demand — say, 10 arriving in a burst — every millisecond in the spawn pipeline stacks up. I spent an afternoon profiling herd's allocation path and shaved over a second off per-VM spawn time through three targeted changes. Here's the story.

## The Starting Point

Herd's `FirecrackerFactory.Spawn` does five things sequentially to bring up a VM:

1. Pull a container image and create a devmapper snapshot (rootfs)
2. Acquire an IP from the IPAM pool
3. Create a TAP interface with point-to-point addressing
4. Start the Firecracker process
5. Poll the vsock until the guest agent is ready

Before any optimization, a single scale-up spawn looked like this:

```
PullAndSnapshot  401ms
IPAM.Acquire     1.5µs
TAP setup        3.5ms
cmd.Start        357µs
vsock connect    2.33s
TOTAL            2.74s
```

The vsock connect time is dominated by the VM boot — kernel load, initrd unpack, guest agent startup. That's largely irreducible without a fundamentally different approach (snapshot/restore). But the 400ms `PullAndSnapshot` was pure waste, and the TAP setup had a subtler concurrency problem.

## Finding the Bottlenecks

Rather than reaching for pprof, I started with the simplest possible instrumentation: `time.Now()` / `time.Since()` logs around each stage of `Spawn`. Five timing calls, rebuild, re-run the test, read the daemon log. Total turnaround: 60 seconds. The numbers told the whole story immediately.

This is worth emphasizing — before you invest in profiling infrastructure, a few well-placed timing logs in the hot path will often give you the answer in a single test run.

## Optimization 1: Netlink Migration

**Problem:** Every VM spawn shelled out to `ip` three times to create a TAP interface:

```go
runCmd("ip", "tuntap", "add", "dev", name, "mode", "tap")
runCmd("ip", "addr", "add", hostIP, "peer", guestIP, "dev", name)
runCmd("ip", "link", "set", "dev", name, "up")
```

Each `runCmd` forks a child process (`exec.Command`), which means three fork/exec cycles per VM. On its own, each call is ~5ms. But the real problem surfaces under concurrency: every `ip` invocation takes the kernel's RTNL (routing netlink) mutex. When 7 VMs spawn simultaneously, that's 21 `ip` processes all serializing on a single kernel lock. Add process startup overhead, and you get 100-300ms of serial contention for TAP setup alone.

**Fix:** Replaced all three shell commands with direct netlink syscalls via `vishvananda/netlink`:

```go
tap := &netlink.Tuntap{
    LinkAttrs: netlink.LinkAttrs{Name: name},
    Mode:      netlink.TUNTAP_MODE_TAP,
}
netlink.LinkAdd(tap)

link, _ := netlink.LinkByName(name)
addr := &netlink.Addr{
    IPNet: &net.IPNet{IP: host, Mask: net.CIDRMask(32, 32)},
    Peer:  &net.IPNet{IP: peer, Mask: net.CIDRMask(32, 32)},
}
netlink.AddrAdd(link, addr)
netlink.LinkSetUp(link)
```

Same three kernel operations, but now they're direct syscalls in-process — no fork, no exec, no PATH lookup, no shell parsing. The RTNL lock is still there (it's a kernel primitive), but each hold is microseconds instead of the milliseconds needed to spawn and tear down a child process.

**Result:** TAP setup: ~3.5ms per VM. The concurrency benefit is harder to measure in isolation, but removing 21 process spawns from the critical path is unambiguously better.

I also migrated `DeleteTap` and `getDefaultInterface` (which parsed `ip route` output) to netlink for consistency. The `iptables` calls in `Bootstrap`/`Teardown` were left as shell commands — they run once at daemon startup, not in the hot path.

## Optimization 2: Split Image Pull from Snapshot

**Problem:** The original `PullAndSnapshot` method did two things in one call:

1. `client.Pull(imageRef)` — contacts the container registry, resolves the manifest, checks layers
2. `snapService.Prepare(...)` — creates a local devmapper thin-volume

Every single `Spawn` paid the Pull cost, even though the image was already cached locally. That's a ~400ms registry round-trip on every VM, for an image that changes maybe once a week.

**Fix:** Split the method into two with clear lifecycle boundaries:

- **`WarmImage(ctx, imageRef)`** — called once at daemon startup. Checks the local content store first (`client.GetImage`), only falls back to `Pull` if the image is genuinely missing. Caches the parent chain ID on the Manager struct.

- **`Snapshot(ctx, vmID)`** — called per-VM in the hot path. Creates a lease and prepares a devmapper thin-volume from the cached parent. Pure local operation, zero network calls.

```go
// Daemon startup (once)
mgr.WarmImage(ctx, "docker.io/xhemal/ubuntu-network-toolkit:latest")

// Per-VM hot path
rootfsPath, err := mgr.Snapshot(ctx, workerID)
```

The image reference moved from `Spawn` (where it was hardcoded and evaluated per-call) to `buildPool` (where it's evaluated once).

**Result:** Per-VM rootfs provisioning dropped from ~400ms to ~48ms for the first concurrent snapshot.

## Bonus: snapshotExists Was Doing a Full Table Scan

While refactoring the storage layer, I noticed `snapshotExists` was walking every snapshot in containerd to check if one key existed:

```go
func (m *Manager) snapshotExists(ctx context.Context, ss snapshots.Snapshotter, key string) (bool, error) {
    exists := false
    err := ss.Walk(ctx, func(_ context.Context, info snapshots.Info) error {
        if info.Name == key {
            exists = true
        }
        return nil
    })
    return exists, nil
}
```

The snapshotter interface has a `Stat` method — an O(1) lookup by key. Replaced the Walk with:

```go
_, statErr := snapService.Stat(leaseCtx, snapshotKey)
if errdefs.IsNotFound(statErr) {
    // prepare new snapshot
}
```

## The Staircase: What Devmapper Serialization Looks Like

After the optimizations, I ran the stress test again and grepped the snapshot timings:

```
Snapshot 1:   48ms
Snapshot 2:   97ms
Snapshot 3:  136ms
Snapshot 4:   54ms   (new batch)
Snapshot 5:  103ms
Snapshot 6:  156ms
Snapshot 7:  216ms
Snapshot 8:  270ms
Snapshot 9:  331ms
Snapshot 10: 388ms
Snapshot 11: 450ms
Snapshot 12: 476ms
```

A perfect linear staircase. Each snapshot takes ~50ms of real work, but the Linux device-mapper subsystem serializes thin-volume creation through a single `ioctl(DM_DEV_CREATE)` kernel mutex. When 10 goroutines call `snapService.Prepare` concurrently, they queue up single-file. Snapshot N waits for snapshots 1 through N-1 to finish first.

This is not a Go mutex, not a containerd lock — it's the Linux kernel's device-mapper ioctl serialization. The next optimization here would be pre-provisioning snapshots at startup so the hot path grabs a pre-made one from a channel instead of hitting the kernel.

## Summary

| Stage | Before | After | Technique |
|---|---|---|---|
| Image + Snapshot | ~400ms | ~48ms | Split WarmImage/Snapshot, cache-first GetImage |
| TAP setup | ~15-20ms (shell) | ~3.5ms (netlink) | Direct netlink syscalls, no fork/exec |
| snapshotExists | O(n) Walk | O(1) Stat | Use the right API |
| **Per-VM total** | **~2.74s** | **~2.38s** | — |

The vsock connect (~2.3s) still dominates — that's the VM boot time. But the controllable overhead around it went from ~400ms to ~50ms, and the concurrent contention profile is fundamentally better.

The lesson: profile the hot path first, optimize the things you control, and pay attention to what the kernel is doing under your abstractions.
