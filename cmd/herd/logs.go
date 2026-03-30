package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"

	"github.com/herd-core/herd/internal/config"
	"github.com/spf13/cobra"
)

var (
	// uses global configPath
)

var logsCmd = &cobra.Command{
	Use:   "logs [session_id]",
	Short: "Stream logs from a MicroVM session",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		sessionID := args[0]
		
		cfg, err := config.Load(configPath)
		if err != nil {
			log.Fatalf("failed to load config %q: %v", configPath, err)
		}
		
		url := fmt.Sprintf("http://%s/v1/sessions/%s/logs", cfg.Network.ControlBind, sessionID)
		resp, err := http.Get(url)
		if err != nil {
			log.Fatalf("failed to fetch logs: %v", err)
		}
		defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to close log stream body: %v\n", cerr)
		}
	}()

		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(resp.Body)
			log.Fatalf("daemon rejected request (status %v): %s", resp.Status, body)
		}

		_, err = io.Copy(os.Stdout, resp.Body)
		if err != nil {
			log.Fatalf("stream interrupted: %v", err)
		}
	},
}

func init() {
	rootCmd.AddCommand(logsCmd)
}
