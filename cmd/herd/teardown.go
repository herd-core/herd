package main

import (
	"log"

	"github.com/herd-core/herd/internal/config"
	"github.com/herd-core/herd/internal/network"
	"github.com/herd-core/herd/internal/storage"
	"github.com/spf13/cobra"
)

var (
	teardownConfigPath string
)

var teardownCmd = &cobra.Command{
	Use:   "teardown",
	Short: "Tear down bootstrapped storage state",
	Long:  `Stops isolated containerd, removes the thin-pool, detaches loop devices, and purges state directory artifacts.`,
	Run: func(cmd *cobra.Command, args []string) {
		runTeardown()
	},
}

func init() {
	rootCmd.AddCommand(teardownCmd)
	teardownCmd.Flags().StringVar(&teardownConfigPath, "config", "/etc/herd/config.yaml", "Path to daemon configuration file")
}

func runTeardown() {
	cfg, err := config.Load(teardownConfigPath)
	if err != nil {
		log.Fatalf("failed to load config %q: %v", teardownConfigPath, err)
	}

	if err := storage.Teardown(cfg.Storage.StateDir); err != nil {
		log.Fatalf("failed to teardown storage: %v", err)
	}

	if err := network.Teardown(); err != nil {
		log.Fatalf("failed to teardown nat routing: %v", err)
	}

	log.Println("Teardown completed successfully.")
}
