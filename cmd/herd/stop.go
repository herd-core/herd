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

var stopCmd = &cobra.Command{
	Use:   "stop [session_id]",
	Short: "Stop a running MicroVM session",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		sessionID := args[0]
		
		cfg, err := config.Load(configPath)
		if err != nil {
			log.Fatalf("failed to load config %q: %v", configPath, err)
		}
		
		url := fmt.Sprintf("http://%s/v1/sessions/%s", cfg.Network.ControlBind, sessionID)
		
		client := &http.Client{}
		req, err := http.NewRequest(http.MethodDelete, url, nil)
		if err != nil {
			log.Fatalf("failed to create request: %v", err)
		}
		
		resp, err := client.Do(req)
		if err != nil {
			log.Fatalf("failed to send request: %v", err)
		}
		defer func() {
			if cerr := resp.Body.Close(); cerr != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to close response body: %v\n", cerr)
			}
		}()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			log.Fatalf("daemon rejected request (status %v): %s", resp.Status, body)
		}

		fmt.Printf("Successfully stopped session %s\n", sessionID)
	},
}

func init() {
	rootCmd.AddCommand(stopCmd)
}
