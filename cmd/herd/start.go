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

	pool, err := buildPool(cfg)
	if err != nil {
		log.Fatalf("failed to initialize pool: %v", err)
	}
	defer func() {
		if err := pool.Shutdown(context.Background()); err != nil {
			log.Printf("Error shutting down pool: %v", err)
		}
	}()

	controlLis, err := daemon.ListenUnixSocket(cfg.Network.ControlSocket)
	if err != nil {
		log.Fatalf("failed to create control socket listener: %v", err)
	}
	defer func() {
		if err := controlLis.Close(); err != nil {
			log.Printf("Error closing control listener: %v", err)
		}
		if err := daemon.RemoveUnixSocket(cfg.Network.ControlSocket); err != nil {
			log.Printf("Error cleaning up control socket: %v", err)
		}
	}()

	grpcServer := grpc.NewServer()
	pb.RegisterHerdServiceServer(grpcServer, daemon.NewServer(pool, "http://"+cfg.Network.DataBind, cfg.Resources.MaxWorkers))

	httpServer := &http.Server{
		Addr:    cfg.Network.DataBind,
		Handler: daemon.NewDataPlaneHandler(pool, cfg.Telemetry.MetricsPath),
	}

	errCh := make(chan error, 2)

	go func() {
		log.Printf("gRPC control plane listening on unix://%s", cfg.Network.ControlSocket)
		if err := grpcServer.Serve(controlLis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			errCh <- err
		}
	}()

	go func() {
		log.Printf("HTTP data plane listening on http://%s", cfg.Network.DataBind)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	log.Printf("HTTP proxy will listen on %s", cfg.Network.DataBind)
	log.Printf("gRPC socket path will be: %s", cfg.Network.ControlSocket)

	log.Println("Daemon is running. Press CTRL+C to stop.")

	select {
	case <-ctx.Done():
		log.Println("Received shutdown signal. Gracefully stopping...")
	case err := <-errCh:
		log.Printf("Daemon listener failed: %v", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP shutdown error: %v", err)
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
		grpcServer.Stop()
	}

	log.Println("Daemon gracefully stopped.")
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
		herd.WithAutoScale(cfg.Resources.MinWorkers, cfg.Resources.MaxWorkers),
		herd.WithTTL(cfg.Resources.TTLDuration()),
		herd.WithHealthInterval(cfg.Resources.HealthIntervalDuration()),
		herd.WithWorkerReuse(cfg.Resources.WorkerReuse),
	)
}
