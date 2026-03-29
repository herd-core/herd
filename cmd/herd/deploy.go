package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/spf13/cobra"
)

var (
	deployImage   string
	deployTimeout int
)

var deployCmd = &cobra.Command{
	Use:   "deploy",
	Short: "Deploy a new MicroVM session",
	Run: func(cmd *cobra.Command, args []string) {
		reqBody, _ := json.Marshal(map[string]any{
			"image":                deployImage,
			"idle_timeout_seconds": deployTimeout,
		})

		resp, err := http.Post("http://127.0.0.1:8080/v1/sessions", "application/json", bytes.NewReader(reqBody))
		if err != nil {
			log.Fatalf("failed to deploy: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(resp.Body)
			log.Fatalf("daemon rejected request (status %v): %s", resp.Status, body)
		}

		var result map[string]any
		json.NewDecoder(resp.Body).Decode(&result)
		fmt.Printf("Successfully deployed MicroVM!\n")
		fmt.Printf("Session ID: %v\n", result["session_id"])
		fmt.Printf("Internal IP: %v\n", result["internal_ip"])
		fmt.Printf("Proxy URL: %v\n", result["proxy_address"])
	},
}

func init() {
	rootCmd.AddCommand(deployCmd)
	deployCmd.Flags().StringVar(&deployImage, "image", "docker.io/library/alpine:latest", "Image to deploy")
	deployCmd.Flags().IntVar(&deployTimeout, "timeout", 300, "Idle timeout in seconds")
}
