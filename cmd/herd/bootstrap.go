package main

import (
	"log"

	"github.com/herd-core/herd/internal/config"
	"github.com/herd-core/herd/internal/network"
	"github.com/herd-core/herd/internal/storage"
	"github.com/spf13/cobra"
)

var (
	bootstrapConfigPath string
)

var bootstrapCmd = &cobra.Command{
	Use:   "bootstrap",
	Short: "Bootstrap the system requirements",
	Long:  `Bootstraps the required loop devices, devicemapper thin pools, and containerd configuration.`,
	Run: func(cmd *cobra.Command, args []string) {
		runBootstrap()
	},
}

func init() {
	rootCmd.AddCommand(bootstrapCmd)
	bootstrapCmd.Flags().StringVar(&bootstrapConfigPath, "config", "/etc/herd/config.yaml", "Path to daemon configuration file")
}

func runBootstrap() {
	cfg, err := config.Load(bootstrapConfigPath)
	if err != nil {
		log.Fatalf("failed to load config %q: %v", bootstrapConfigPath, err)
	}

	err = storage.Bootstrap(cfg.Storage.StateDir)
	if err != nil {
		log.Fatalf("failed to bootstrap storage: %v", err)
	}

	if err := network.Bootstrap(); err != nil {
		log.Fatalf("failed to bootstrap host nat routing: %v", err)
	}

	log.Println("Bootstrap completed successfully. Containerd is running. You can now run herd start.")
}
