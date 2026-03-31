package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"text/tabwriter"
	"time"

	"github.com/herd-core/herd/internal/config"
	"github.com/spf13/cobra"
)

type SessionStatus struct {
	SessionID            string    `json:"session_id"`
	CreatedAt            time.Time `json:"created_at"`
	LastControlHeartbeat time.Time `json:"last_control_heartbeat"`
	ActiveConns          int       `json:"active_conns"`
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show status of all active sessions",
	Run: func(cmd *cobra.Command, args []string) {
		cfg, err := config.Load(configPath)
		if err != nil {
			log.Fatalf("failed to load config %q: %v", configPath, err)
		}

		url := fmt.Sprintf("http://%s/v1/sessions", cfg.Network.ControlBind)
		resp, err := http.Get(url)
		if err != nil {
			log.Fatalf("failed to fetch status: %v", err)
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

		var sessions []SessionStatus
		if err := json.NewDecoder(resp.Body).Decode(&sessions); err != nil {
			log.Fatalf("failed to decode response: %v", err)
		}

		if len(sessions) == 0 {
			fmt.Println("No active sessions.")
			return
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "SESSION ID\tCREATED\tLAST HEARTBEAT\tCONNS")
		for _, s := range sessions {
			fmt.Fprintf(w, "%s\t%s\t%s\t%d\n",
				s.SessionID,
				s.CreatedAt.Format(time.RFC3339),
				s.LastControlHeartbeat.Format(time.RFC3339),
				s.ActiveConns,
			)
		}
		w.Flush()
	},
}

func init() {
	rootCmd.AddCommand(statusCmd)
}
