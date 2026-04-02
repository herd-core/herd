package main

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"time"

	"github.com/herd-core/herd"
	"github.com/herd-core/herd/internal/config"
	"github.com/herd-core/herd/internal/network"
	"github.com/herd-core/herd/internal/storage"
	"github.com/herd-core/herd/internal/uid"
	"github.com/containerd/containerd"
)

func main() {
	cwd := "/home/hackstrix/herd"
	cfg, err := config.Load("/tmp/herd_cfg_f585f6af.yaml")
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	sockPath := filepath.Join(cfg.Storage.StateDir, "containerd.sock")
	client, err := containerd.New(sockPath)
	if err != nil {
		log.Fatalf("failed to connect to containerd: %v", err)
	}

	mgr := storage.NewManager(client, cfg.Storage.Namespace, cfg.Storage.SnapshotterName)
	if err := mgr.WarmImage(context.Background(), "docker.io/xhemal/ubuntu-network-toolkit:latest"); err != nil {
		log.Fatalf("failed to warm base image: %v", err)
	}

	ipam, err := network.NewIPAM("10.200.0.0/16")
	if err != nil {
		log.Fatalf("failed to init IPAM: %v", err)
	}

	uidPool, err := uid.NewPool(300000, 10)
	if err != nil {
		log.Fatalf("failed to create uid pool: %v", err)
	}

	factory := &herd.FirecrackerFactory{
		FirecrackerPath:     "/home/hackstrix/firecracker-v15.0/firecracker",
		JailerPath:          "/home/hackstrix/firecracker-v15.0/jailer",
		KernelImagePath:     filepath.Join(cwd, "assets/vmlinux.bin"),
		Storage:             mgr,
		GuestAgentPath:      filepath.Join(cwd, "herd-guest-agent"),
		IPAM:                ipam,
		UIDPool:             uidPool,
		JailerChrootBaseDir: "/srv/jailer",
	}

	for i := 0; i < 3; i++ {
		t0 := time.Now()
		sessionID := fmt.Sprintf("test-boot-%d", i)
		worker, err := factory.Spawn(context.Background(), sessionID, herd.TenantConfig{
			Image:   "docker.io/xhemal/ubuntu-network-toolkit:latest",
			Command: []string{"/bin/sh"},
		})
		if err != nil {
			log.Fatalf("Spawn error: %v", err)
		}
		fmt.Printf("Spawn %d took %v\n", i, time.Since(t0))
		_ = worker.Close()
	}
}
