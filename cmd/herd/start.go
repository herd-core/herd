package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
"path/filepath"

"fmt"
	"github.com/containerd/containerd"
	"github.com/herd-core/herd/internal/storage"

	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/herd-core/herd"
	"github.com/herd-core/herd/internal/cloud"
	"github.com/herd-core/herd/internal/config"
	"github.com/herd-core/herd/internal/daemon"
	"github.com/herd-core/herd/internal/lifecycle"
	"github.com/herd-core/herd/internal/network"
	"github.com/spf13/cobra"
)

var (
	// uses global configPath
)

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the herd daemon",
	Long:  `Starts the herd daemon, exposing a gRPC Control Plane on a Unix socket and an HTTP Data Plane proxy.`,
	Run: func(cmd *cobra.Command, args []string) {
		runDaemon()
	},
}

func init() {
	rootCmd.AddCommand(startCmd)
}

func runDaemon() {
	// 1. Setup graceful shutdown context
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Println("Starting herd daemon...")

	if err := daemon.EnforceRuntimePolicy(runtime.GOOS, log.Default()); err != nil {
		log.Fatal(err)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("failed to load config %q: %v", configPath, err)
	}
	eventLogger := daemon.NewEventLogger(cfg.Telemetry.LogFormat, log.Default())
	eventLogger.Info("daemon_starting", map[string]any{
		"config_path":      configPath,
		"control_bind":     cfg.Network.ControlBind,
		"data_bind":        cfg.Network.DataBind,
		"metrics_path":     cfg.Telemetry.MetricsPath,
		"telemetry_format": cfg.Telemetry.LogFormat,
	})

	pool, err := buildPool(cfg)
	if err != nil {
		log.Fatalf("failed to initialize pool: %v", err)
	}
	defer func() {
		if err := pool.Shutdown(context.Background()); err != nil {
			eventLogger.Error("pool_shutdown_failed", map[string]any{"error": err})
		}
	}()

	controlLis, err := net.Listen("tcp", cfg.Network.ControlBind)
	if err != nil {
		log.Fatalf("failed to create control socket listener: %v", err)
	}
	defer func() {
		if err := controlLis.Close(); err != nil {
			eventLogger.Error("control_listener_close_failed", map[string]any{"error": err})
		}
	}()
	
	// Initialize Cloud Control Plane (Optional)
	if cfg.Cloud.Enabled {
		cloudClient := cloud.NewClient(cfg.Cloud)
		if err := cloudClient.Start(ctx); err != nil {
			eventLogger.Error("cloud_control_connection_failed", map[string]any{"error": err})
		} else {
			defer cloudClient.Close()
		}
	}

	// Initialize Lifecycle Manager
	lm := lifecycle.NewManager(pool)
	go lm.StartReaper(ctx) // Run reaper in background

	controlServer := &http.Server{
		Handler: daemon.NewControlPlaneHandler(pool, lm, "http://"+cfg.Network.DataBind, eventLogger),
	}

	httpServer := &http.Server{
		Addr:    cfg.Network.DataBind,
		Handler: daemon.NewDataPlaneHandler(pool, lm, cfg.Telemetry.MetricsPath),
	}

	errCh := make(chan error, 2)

	go func() {
		eventLogger.Info("control_plane_listening", map[string]any{"address": "http://" + cfg.Network.ControlBind})
		if err := controlServer.Serve(controlLis); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	go func() {
		eventLogger.Info("data_plane_listening", map[string]any{"address": "http://" + cfg.Network.DataBind})
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	eventLogger.Info("daemon_running", map[string]any{})

	select {
	case <-ctx.Done():
		eventLogger.Info("daemon_shutdown_signal_received", map[string]any{})
	case err := <-errCh:
		eventLogger.Error("daemon_listener_failed", map[string]any{"error": err})
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		eventLogger.Error("data_plane_shutdown_failed", map[string]any{"error": err})
	}

	var once sync.Once
	done := make(chan struct{})
	go func() {
		once.Do(func() {
			if err := controlServer.Shutdown(shutdownCtx); err != nil {
				log.Printf("error: daemon control plane shutdown failed: %v", err)
			}
		})
		close(done)
	}()

	select {
	case <-done:
	case <-shutdownCtx.Done():
		eventLogger.Warn("control_plane_graceful_shutdown_timeout", map[string]any{})
		if err := controlServer.Close(); err != nil {
			log.Printf("error: failed to close control server: %v", err)
		}
	}

	eventLogger.Info("daemon_stopped", map[string]any{})
}

func buildPool(cfg *config.Config) (*herd.Pool[*http.Client], error) {

	sockPath := filepath.Join(cfg.Storage.StateDir, "containerd.sock")
	absSock, err := filepath.Abs(sockPath)
	if err != nil {
		return nil, fmt.Errorf("resolve containerd socket path: %w", err)
	}
	client, err := containerd.New(absSock)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to containerd at %s: %w", absSock, err)
	}

	mgr := storage.NewManager(client, cfg.Storage.Namespace, cfg.Storage.SnapshotterName)

	ipam, err := network.NewIPAM("10.200.0.0/16")
	if err != nil {
		return nil, fmt.Errorf("failed to initialize IPAM: %w", err)
	}

	factory := &herd.FirecrackerFactory{
		FirecrackerPath: cfg.Binaries.FirecrackerPath,
		KernelImagePath: cfg.Binaries.KernelImagePath,
		Storage:         mgr,
		SocketPathDir:   "/tmp",
		GuestAgentPath:  cfg.Binaries.GuestAgentPath,
		IPAM:            ipam,
	}

	return herd.New(factory,
		herd.WithMaxWorkers(cfg.Resources.MaxGlobalVMs),
	)
}
