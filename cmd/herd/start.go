package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/herd-core/herd"
	"github.com/herd-core/herd/internal/config"
	"github.com/herd-core/herd/internal/daemon"
	"github.com/spf13/cobra"
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
		log.Println("Shutting down pool...")
		// Will assume Shutdown(context.Background()) exists, we'll verify this during compilation check.
		// If Pool does not have Shutdown, we'll update this.
		err := pool.Shutdown(context.Background())
		if err != nil {
			log.Println("Error shutting down pool:", err)
		}
		log.Println("Daemon gracefully stopped.")
	}()

	log.Printf("HTTP proxy will listen on %s", cfg.Network.DataBind)
	log.Printf("gRPC socket path will be: %s", cfg.Network.ControlSocket)

	log.Println("Daemon is running. Press CTRL+C to stop.")

	// Block until signal is received
	<-ctx.Done()
	log.Println("Received shutdown signal. Gracefully stopping...")
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
