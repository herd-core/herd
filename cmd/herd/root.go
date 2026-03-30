package main

import (
	"path/filepath"

	"github.com/herd-core/herd/internal/config"
	"github.com/spf13/cobra"
)

var (
	configPath string
)

var rootCmd = &cobra.Command{
	Use:   "herd",
	Short: "herd is a blazing fast sidecar daemon for process pooling",
	Long:  `herd transforms passive Go libraries into a production-grade cross-language daemon built for high-performance process pooling.`,
}

func init() {
	defaultConfig := "/etc/herd/herd.yaml"
	if home, err := config.GetTargetHomeDir(); err == nil {
		defaultConfig = filepath.Join(home, ".herd", "herd.yaml")
	}
	rootCmd.PersistentFlags().StringVar(&configPath, "config", defaultConfig, "Path to daemon configuration file")
}

// Execute adds all child commands to the root command and sets flags appropriately.
func Execute() error {
	return rootCmd.Execute()
}
