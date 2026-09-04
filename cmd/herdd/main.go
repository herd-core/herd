package main

import (
	"log"
	"os"
	"path/filepath"

	"github.com/herd-core/herd/internal/config"
	"github.com/spf13/cobra"
)

var (
	configPath string
)

func main() {
	if err := Execute(); err != nil {
		log.Println(err)
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:   "herdd",
	Short: "herdd is a blazing daemon meant for running Firecracker MicroVMs",
	Long:  `herdd allows to run blazingly fast microvm's for high performance secure computing`,
}

func init() {
	defaultConfig := "/etc/herd/herd.yaml"
	if home, err := config.GetTargetHomeDir(); err == nil {
		defaultConfig = filepath.Join(home, ".herd", "herd.yaml")
	}
	rootCmd.PersistentFlags().StringVar(&configPath, "config", defaultConfig, "Path to daemon configuration file")
}

func Execute() error {
	return rootCmd.Execute()
}