package storage

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/mount"

	"github.com/containerd/containerd/leases"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/snapshots"
	"github.com/opencontainers/image-spec/identity"
)

type Manager struct {
	client      *containerd.Client
	namespace   string
	snapshotter string
}

func NewManager(client *containerd.Client, namespace, snapshotter string) *Manager {
	return &Manager{
		client:      client,
		namespace:   namespace,
		snapshotter: snapshotter,
	}
}

// PullImage pulls the given image reference and creates a devmapper snapshot for it.
// It returns the physical path to the block device.
func (m *Manager) PullAndSnapshot(ctx context.Context, imageRef, vmID string) (string, error) {
	slog.Info("pulling image", "ref", imageRef, "vmID", vmID)
	nsCtx := namespaces.WithNamespace(ctx, m.namespace)

	// Create a lease to protect the image and snapshot from GC
	leaseCtx, err := m.withLease(nsCtx, vmID)
	if err != nil {
		return "", fmt.Errorf("failed to create lease for %s: %w", vmID, err)
	}

	// Pull the image
	image, err := m.client.Pull(leaseCtx, imageRef, containerd.WithPullUnpack)
	if err != nil {
		return "", fmt.Errorf("failed to pull image %s: %w", imageRef, err)
	}

	// Ensure it's unpacked with the thin-pool snapshotter
	unpacked, err := image.IsUnpacked(leaseCtx, m.snapshotter)
	if err != nil {
		return "", fmt.Errorf("failed to check if image is unpacked: %w", err)
	}
	if !unpacked {
		slog.Debug("unpacking image", "snapshotter", m.snapshotter)
		if err := image.Unpack(leaseCtx, m.snapshotter); err != nil {
			return "", fmt.Errorf("failed to unpack image: %w", err)
		}
	}

	// Read image rootfs content to get the parent chain ID
	rootFS, err := image.RootFS(leaseCtx)
	if err != nil {
		return "", fmt.Errorf("failed to get rootfs: %w", err)
	}
	parent := identity.ChainID(rootFS).String()

	snapshotKey := m.snapshotKey(vmID)
	snapService := m.client.SnapshotService(m.snapshotter)

	// Check if this specific snapshot exists
	exists, err := m.snapshotExists(leaseCtx, snapService, snapshotKey)
	if err != nil {
		return "", err
	}

	// Prepare snapshot if it doesn't exist
	var mounts []mount.Mount
	if !exists {
		slog.Debug("preparing devmapper snapshot", "key", snapshotKey)
		mounts, err = snapService.Prepare(leaseCtx, snapshotKey, parent, snapshots.WithLabels(map[string]string{
			"herd/vmID": vmID,
		}))
		if err != nil {
			return "", fmt.Errorf("failed to prepare snapshot: %w", err)
		}
	} else {
		mounts, err = snapService.Mounts(leaseCtx, snapshotKey)
		if err != nil {
			return "", fmt.Errorf("failed to get mounts for snapshot: %w", err)
		}
	}

	// Ensure it's a devmapper mount and extract the physical block device path
	for _, mnt := range mounts {
		if mnt.Type == "devmapper" || mnt.Type == "bind" {
			// In devmapper setups, the generic bind or devmapper mount types return the device path in Source.
			if mnt.Source != "" {
				slog.Info("extracted block device", "path", mnt.Source)
				return mnt.Source, nil
			}
		}
	}

	return "", fmt.Errorf("failed to extract block device path from devmapper mounts")
}

// Teardown safely releases the VM's lease, allowing containerd's garbage collector to destroy the block device.
func (m *Manager) Teardown(ctx context.Context, vmID string) error {
	slog.Info("tearing down storage", "vmID", vmID)
	nsCtx := namespaces.WithNamespace(ctx, m.namespace)
	
	// Remove snapshot
	snapService := m.client.SnapshotService(m.snapshotter)
	snapshotKey := m.snapshotKey(vmID)
	
	if err := snapService.Remove(nsCtx, snapshotKey); err != nil {
		slog.Warn("failed to remove snapshot", "error", err)
	}

	// Delete lease
	ctxWithLease, err := m.withLease(nsCtx, vmID)
	if err == nil {
		leaseStr, ok := leases.FromContext(ctxWithLease)
		if ok {
			ls := m.client.LeasesService()
			if err := ls.Delete(nsCtx, leases.Lease{ID: leaseStr}); err != nil {
				return fmt.Errorf("failed to delete lease %s: %w", leaseStr, err)
			}
		}
	}
	
	return nil
}

func (m *Manager) snapshotKey(vmID string) string {
	return fmt.Sprintf("herd-vm-%s", vmID)
}

func (m *Manager) withLease(ctx context.Context, vmID string) (context.Context, error) {
	ls := m.client.LeasesService()
	leaseID := fmt.Sprintf("lease-vm-%s", vmID)
	
	// Try creating
	l, err := ls.Create(ctx, leases.WithID(leaseID))
	if err != nil {
		// If exists, verify and use it
		existing, listErr := ls.List(ctx, fmt.Sprintf("id==%s", leaseID))
		if listErr == nil && len(existing) > 0 {
			return leases.WithLease(ctx, existing[0].ID), nil
		}
		return nil, err
	}
	
	return leases.WithLease(ctx, l.ID), nil
}

func (m *Manager) snapshotExists(ctx context.Context, ss snapshots.Snapshotter, key string) (bool, error) {
	exists := false
	err := ss.Walk(ctx, func(_ context.Context, info snapshots.Info) error {
		if info.Name == key {
			exists = true
		}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("walking snapshots: %w", err)
	}
	return exists, nil
}
