package storage

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/leases"
	"github.com/containerd/containerd/mount"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/snapshots"
	"github.com/opencontainers/image-spec/identity"
	"golang.org/x/sys/unix"
)

// Manager mediates between herd and containerd for rootfs provisioning.
//
// Lifecycle:
//  1. Call WarmImage once at daemon startup to pull/cache the base image.
//  2. Call Snapshot per-VM to create a copy-on-write block device from the cached image.
//  3. Call Teardown per-VM when the VM is destroyed.
type Manager struct {
	client      *containerd.Client
	namespace   string
	snapshotter string

	// parentChainID is the content-addressable identifier of the fully-unpacked
	// image layer chain. Set by WarmImage, consumed by Snapshot. This avoids
	// resolving the image on every Spawn — a ~400ms registry round-trip.
	parentChainID string
}

func NewManager(client *containerd.Client, namespace, snapshotter string) *Manager {
	return &Manager{
		client:      client,
		namespace:   namespace,
		snapshotter: snapshotter,
	}
}

// ---------------------------------------------------------------------------
// Startup path (called once)
// ---------------------------------------------------------------------------

// WarmImage ensures the base image is present in the local content store
// and unpacked for the configured snapshotter. It caches the parent chain ID
// so that subsequent Snapshot calls never touch the network.
//
// Call this once at daemon startup before creating the pool.
func (m *Manager) WarmImage(ctx context.Context, imageRef string) error {
	nsCtx := namespaces.WithNamespace(ctx, m.namespace)

	image, err := m.client.GetImage(nsCtx, imageRef)
	if err != nil {
		if !errdefs.IsNotFound(err) {
			return fmt.Errorf("check local image %s: %w", imageRef, err)
		}
		slog.Info("image not cached, pulling from registry", "ref", imageRef)
		image, err = m.client.Pull(nsCtx, imageRef, containerd.WithPullUnpack)
		if err != nil {
			return fmt.Errorf("pull image %s: %w", imageRef, err)
		}
	}

	unpacked, err := image.IsUnpacked(nsCtx, m.snapshotter)
	if err != nil {
		return fmt.Errorf("check unpack status for %s: %w", imageRef, err)
	}
	if !unpacked {
		slog.Info("unpacking image for snapshotter", "ref", imageRef, "snapshotter", m.snapshotter)
		if err := image.Unpack(nsCtx, m.snapshotter); err != nil {
			return fmt.Errorf("unpack image %s: %w", imageRef, err)
		}
	}

	rootFS, err := image.RootFS(nsCtx)
	if err != nil {
		return fmt.Errorf("read rootfs for %s: %w", imageRef, err)
	}
	m.parentChainID = identity.ChainID(rootFS).String()

	slog.Info("image warmed", "ref", imageRef, "parent", m.parentChainID)
	return nil
}

// ---------------------------------------------------------------------------
// Hot path (called per-VM)
// ---------------------------------------------------------------------------

// Snapshot creates a copy-on-write devmapper thin-volume for a single VM,
// derived from the parent image cached by WarmImage.
//
// It returns the host path to the block device (e.g. /dev/dm-X).
// This is a pure local operation — no registry or network calls.
func (m *Manager) Snapshot(ctx context.Context, vmID string) (string, error) {
	if m.parentChainID == "" {
		return "", fmt.Errorf("storage: Snapshot called before WarmImage")
	}

	nsCtx := namespaces.WithNamespace(ctx, m.namespace)

	leaseCtx, err := m.withLease(nsCtx, vmID)
	if err != nil {
		return "", fmt.Errorf("create lease for %s: %w", vmID, err)
	}

	snapshotKey := m.snapshotKey(vmID)
	snapService := m.client.SnapshotService(m.snapshotter)

	var mounts []mount.Mount

	_, statErr := snapService.Stat(leaseCtx, snapshotKey)
	if errdefs.IsNotFound(statErr) {
		mounts, err = snapService.Prepare(leaseCtx, snapshotKey, m.parentChainID, snapshots.WithLabels(map[string]string{
			"herd/vmID": vmID,
		}))
		if err != nil {
			return "", fmt.Errorf("prepare snapshot %s: %w", snapshotKey, err)
		}
	} else if statErr != nil {
		return "", fmt.Errorf("stat snapshot %s: %w", snapshotKey, statErr)
	} else {
		mounts, err = snapService.Mounts(leaseCtx, snapshotKey)
		if err != nil {
			return "", fmt.Errorf("get mounts for snapshot %s: %w", snapshotKey, err)
		}
	}

	devPath, ok := extractBlockDeviceFromMounts(mounts)
	if !ok {
		return "", fmt.Errorf("no block device in mounts for snapshot %s", snapshotKey)
	}

	slog.Debug("snapshot ready", "vmID", vmID, "dev", devPath)
	return devPath, nil
}

// DefaultGuestAgentPath is where the binary is placed inside the VM rootfs and
// must match the kernel boot arg init=...
const DefaultGuestAgentPath = "/usr/local/bin/herd-guest-agent"

// InjectGuestAgent mounts the snapshot's root filesystem on the host, copies a
// static herd-guest-agent binary into the image, then unmounts. Required for
// initrd-less boot (kernel runs init from ext4).
func (m *Manager) InjectGuestAgent(ctx context.Context, vmID, hostBinaryPath, guestPath string) error {
	if guestPath == "" {
		guestPath = DefaultGuestAgentPath
	}
	st, err := os.Stat(hostBinaryPath)
	if err != nil {
		return fmt.Errorf("stat host guest agent %s: %w", hostBinaryPath, err)
	}
	if st.IsDir() {
		return fmt.Errorf("host guest agent path is a directory: %s", hostBinaryPath)
	}

	nsCtx := namespaces.WithNamespace(ctx, m.namespace)
	leaseCtx, err := m.withLease(nsCtx, vmID)
	if err != nil {
		return fmt.Errorf("lease for inject %s: %w", vmID, err)
	}

	snapshotKey := m.snapshotKey(vmID)
	snapService := m.client.SnapshotService(m.snapshotter)
	mounts, err := snapService.Mounts(leaseCtx, snapshotKey)
	if err != nil {
		return fmt.Errorf("mounts for inject %s: %w", snapshotKey, err)
	}
	if len(mounts) == 0 {
		return fmt.Errorf("no mounts for snapshot %s", snapshotKey)
	}

	tmpDir, err := os.MkdirTemp("", "herd-inject-*")
	if err != nil {
		return fmt.Errorf("mkdir temp for inject: %w", err)
	}
	defer func() {
		_ = mount.Unmount(tmpDir, unix.MNT_DETACH)
		_ = os.RemoveAll(tmpDir)
	}()

	if err := mount.All(mounts, tmpDir); err != nil {
		return fmt.Errorf("mount snapshot for inject: %w", err)
	}

	rel := strings.TrimPrefix(guestPath, "/")
	dstPath := filepath.Join(tmpDir, rel)
	if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
		return fmt.Errorf("mkdir guest parent dirs: %w", err)
	}

	src, err := os.Open(hostBinaryPath)
	if err != nil {
		return fmt.Errorf("open host binary: %w", err)
	}
	defer src.Close()

	dst, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return fmt.Errorf("create guest binary: %w", err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return fmt.Errorf("copy guest binary: %w", err)
	}
	if err := dst.Close(); err != nil {
		return fmt.Errorf("close guest binary: %w", err)
	}

	slog.Debug("injected guest agent", "vmID", vmID, "guestPath", guestPath)
	return nil
}

// ---------------------------------------------------------------------------
// Teardown
// ---------------------------------------------------------------------------

// Teardown safely releases the VM's lease, allowing containerd's garbage
// collector to destroy the block device.
func (m *Manager) Teardown(ctx context.Context, vmID string) error {
	slog.Info("tearing down storage", "vmID", vmID)
	nsCtx := namespaces.WithNamespace(ctx, m.namespace)

	snapService := m.client.SnapshotService(m.snapshotter)
	snapshotKey := m.snapshotKey(vmID)

	if err := snapService.Remove(nsCtx, snapshotKey); err != nil {
		slog.Warn("failed to remove snapshot", "error", err)
	}

	ctxWithLease, err := m.withLease(nsCtx, vmID)
	if err == nil {
		leaseStr, ok := leases.FromContext(ctxWithLease)
		if ok {
			ls := m.client.LeasesService()
			if err := ls.Delete(nsCtx, leases.Lease{ID: leaseStr}); err != nil {
				return fmt.Errorf("delete lease %s: %w", leaseStr, err)
			}
		}
	}

	return nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

func (m *Manager) snapshotKey(vmID string) string {
	return fmt.Sprintf("herd-vm-%s", vmID)
}

func (m *Manager) withLease(ctx context.Context, vmID string) (context.Context, error) {
	ls := m.client.LeasesService()
	leaseID := fmt.Sprintf("lease-vm-%s", vmID)

	l, err := ls.Create(ctx, leases.WithID(leaseID))
	if err != nil {
		existing, listErr := ls.List(ctx, fmt.Sprintf("id==%s", leaseID))
		if listErr == nil && len(existing) > 0 {
			return leases.WithLease(ctx, existing[0].ID), nil
		}
		return nil, err
	}

	return leases.WithLease(ctx, l.ID), nil
}

func extractBlockDeviceFromMounts(mounts []mount.Mount) (string, bool) {
	for _, mnt := range mounts {
		if strings.HasPrefix(mnt.Source, "/dev/") {
			return mnt.Source, true
		}
		for _, opt := range mnt.Options {
			if strings.HasPrefix(opt, "device=/dev/") {
				return strings.TrimPrefix(opt, "device="), true
			}
		}
	}
	for _, mnt := range mounts {
		if mnt.Source != "" {
			return mnt.Source, true
		}
	}
	return "", false
}
