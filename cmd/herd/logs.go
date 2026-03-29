package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"

	"github.com/spf13/cobra"
)

var logsCmd = &cobra.Command{
	Use:   "logs [session_id]",
	Short: "Stream logs from a MicroVM session",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		sessionID := args[0]
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:8080/v1/sessions/%s/logs", sessionID))
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
