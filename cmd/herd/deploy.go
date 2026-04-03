package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/herd-core/herd/internal/config"
	"github.com/spf13/cobra"
)

var (
	deployImage   string
	deployTimeout int
	absoluteDeployTimeout int
	deployCommand []string
	deployEnv     []string
	deployPublish []string
)

var deployCmd = &cobra.Command{
	Use:   "deploy",
	Short: "Deploy a new MicroVM session",
	Run: func(cmd *cobra.Command, args []string) {
		// Parse config to find control_bind
		cfg, err := config.Load(configPath)
		if err != nil {
			log.Fatalf("failed to load config %q: %v", configPath, err)
		}
		
		req := map[string]any{
			"image":                deployImage,
			"idle_timeout_seconds": deployTimeout,
			"ttl_seconds": absoluteDeployTimeout,
		}
		if len(deployCommand) > 0 {
			req["command"] = deployCommand
		}
		if len(deployEnv) > 0 {
			envMap := make(map[string]string, len(deployEnv))
			for _, e := range deployEnv {
				k, v, ok := strings.Cut(e, "=")
				if !ok {
					log.Fatalf("invalid env format %q: expected KEY=VALUE", e)
				}
				envMap[k] = v
			}
			req["env"] = envMap
		}

		if len(deployPublish) > 0 {
			mappings := make([]map[string]any, 0, len(deployPublish))
			for _, p := range deployPublish {
				protocol := "tcp"
				addrPart := p
				if protoParts := strings.Split(p, "/"); len(protoParts) == 2 {
					addrPart = protoParts[0]
					protocol = strings.ToLower(protoParts[1])
				}

				m := map[string]any{
					"protocol": protocol,
				}
				parts := strings.Split(addrPart, ":")

				if len(parts) == 2 {
					// host_port:guest_port OR :guest_port
					if parts[0] == "" {
						// :80
						m["host_port"] = 0
						m["host_interface"] = "0.0.0.0"
					} else {
						// 8080:80
						hPort, _ := strconv.Atoi(parts[0])
						m["host_port"] = hPort
						m["host_interface"] = "0.0.0.0"
					}
					gPort, _ := strconv.Atoi(parts[1])
					m["guest_port"] = gPort
				} else if len(parts) == 3 {
					// interface:host_port:guest_port
					m["host_interface"] = parts[0]
					hPort, _ := strconv.Atoi(parts[1])
					gPort, _ := strconv.Atoi(parts[2])
					m["host_port"] = hPort
					m["guest_port"] = gPort
				} else {
					log.Fatalf("invalid publish format %q: expected [[interface:]host_port]:guest_port[/protocol]", p)
				}
				mappings = append(mappings, m)
			}
			req["port_mappings"] = mappings
		}

		reqBody, _ := json.Marshal(req)

		url := fmt.Sprintf("http://%s/v1/sessions", cfg.Network.ControlBind)
		resp, err := http.Post(url, "application/json", bytes.NewReader(reqBody))
		if err != nil {
			log.Fatalf("failed to deploy: %v", err)
		}
		defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to close response body: %v\n", cerr)
		}
	}()

		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(resp.Body)
			log.Fatalf("daemon rejected request (status %v): %s", resp.Status, body)
		}

		var result map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			log.Fatalf("failed to decode response: %v", err)
		}
		fmt.Printf("Successfully deployed MicroVM!\n")
		fmt.Printf("Session ID: %v\n", result["session_id"])
		fmt.Printf("Internal IP: %v\n", result["internal_ip"])
		fmt.Printf("Proxy URL: %v\n", result["proxy_address"])
	},
}

func init() {
	rootCmd.AddCommand(deployCmd)
	deployCmd.Flags().StringVar(&deployImage, "image", "docker.io/library/alpine:latest", "Image to deploy")
	deployCmd.Flags().StringSliceVar(&deployCommand, "cmd", nil, "Command to run inside the VM (e.g. --cmd=/bin/sh,-c,\"echo hello\")")
	deployCmd.Flags().StringArrayVarP(&deployEnv, "env", "e", nil, "Set environment variables (e.g. -e POSTGRES_PASSWORD=secret)")
	deployCmd.Flags().IntVar(&deployTimeout, "timeout", 0, "Idle timeout in seconds")
	deployCmd.Flags().IntVar(&absoluteDeployTimeout, "absolute-timeout", 0, "Absolute timeout in seconds")
	deployCmd.Flags().StringSliceVarP(&deployPublish, "publish", "p", nil, "Publish a port (e.g. 8080:80, :80, 1.2.3.4:8080:80)")
}
