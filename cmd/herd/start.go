package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/herd-core/herd"
	"github.com/herd-core/herd/internal/config"
	"github.com/herd-core/herd/internal/daemon"
	"github.com/herd-core/herd/internal/lifecycle"
	pb "github.com/herd-core/herd/proto/herd/v1"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
)

var (
	configPath string
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
	startCmd.Flags().StringVar(&configPath, "config", "/etc/herd/config.yaml", "Path to daemon configuration file")
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
		"control_socket":   cfg.Network.ControlSocket,
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

	controlLis, err := daemon.ListenUnixSocket(cfg.Network.ControlSocket)
	if err != nil {
		log.Fatalf("failed to create control socket listener: %v", err)
	}
	defer func() {
		if err := controlLis.Close(); err != nil {
			eventLogger.Error("control_listener_close_failed", map[string]any{"error": err})
		}
		if err := daemon.RemoveUnixSocket(cfg.Network.ControlSocket); err != nil {
			eventLogger.Error("control_socket_cleanup_failed", map[string]any{"error": err})
		}
	}()

	// Initialize Lifecycle Manager
	lcConfig := lifecycle.Config{
		AbsoluteTTL:    cfg.Resources.AbsoluteTTLDuration(),
		IdleTTL:        cfg.Resources.IdleTTLDuration(),
		HeartbeatGrace: cfg.Resources.HeartbeatGraceDuration(),
		DataTimeout:    cfg.Resources.DataTimeoutDuration(),
	}
	lm := lifecycle.NewManager(lcConfig, pool)
	go lm.StartReaper(ctx) // Run reaper in background

	grpcServer := grpc.NewServer()
	pb.RegisterHerdServiceServer(grpcServer, daemon.NewServer(pool, lm, "http://"+cfg.Network.DataBind, cfg.Resources.MaxWorkers, eventLogger))

	httpServer := &http.Server{
		Addr:    cfg.Network.DataBind,
		Handler: daemon.NewDataPlaneHandler(pool, lm, cfg.Telemetry.MetricsPath),
	}

	errCh := make(chan error, 2)

	go func() {
		eventLogger.Info("control_plane_listening", map[string]any{"address": "unix://" + cfg.Network.ControlSocket})
		if err := grpcServer.Serve(controlLis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
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
		once.Do(grpcServer.GracefulStop)
		close(done)
	}()

	select {
	case <-done:
	case <-shutdownCtx.Done():
		eventLogger.Warn("control_plane_graceful_shutdown_timeout", map[string]any{})
		grpcServer.Stop()
	}

	eventLogger.Info("daemon_stopped", map[string]any{})
}

func buildPool(cfg *config.Config) (*herd.Pool[*http.Client], error) {
	factory := herd.NewProcessFactory(cfg.Worker.Command[0], cfg.Worker.Command[1:]...).
		WithHealthPath(cfg.Worker.HealthPath).
		WithStartTimeout(cfg.Worker.StartTimeoutDuration()).
		WithStartHealthCheckDelay(cfg.Worker.StartHealthCheckDelayDuration())

	for _, envKV := range cfg.Worker.Env {
		factory.WithEnv(envKV)
	}

	if cfg.Resources.MemoryLimitBytes() > 0 {
		factory.WithMemoryLimit(cfg.Resources.MemoryLimitBytes())
	}
	if cfg.Resources.CPULimitCores > 0 {
		factory.WithCPULimit(cfg.Resources.CPULimitCores)
	}
	if cfg.Resources.PIDsLimit != 0 {
		factory.WithPIDsLimit(cfg.Resources.PIDsLimit)
	}
	if cfg.Resources.InsecureSandbox {
		factory.WithInsecureSandbox()
	}

	return herd.New(factory,
		herd.WithAutoScale(cfg.Resources.TargetIdle, cfg.Resources.MaxWorkers),
		herd.WithTTL(cfg.Resources.IdleTTLDuration()),
		herd.WithHealthInterval(cfg.Resources.HealthIntervalDuration()),
	)
}
