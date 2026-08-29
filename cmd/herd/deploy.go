package main

import (
	"bytes"
	"encoding/json"
	"errors"
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

func splitNetworkBindings(bindings string) (string, int8, int8) {
	// we assume perfect bindings of the format interface:host_port:guest_port
	parts := strings.Split(bindings, ":")
	intface := parts[0]
	hostPort, err := strconv.ParseInt(parts[1], 10, 8) 
	if err != nil {
		fmt.Println("Unable to parse host network bindings")
	}
	guestPort, err := strconv.ParseInt(parts[2], 10, 8)
	if err != nil {
		fmt.Println("Unable to parse guest network bindings")
	}
	return intface, int8(hostPort), int8(guestPort) 
}

func sanitizeNetworkBindings(bindings string) (string, string, error) {
	
	// could be of the format 
	// int:host:guest{/protocol|}
	// host:guest{/protocol|}
	// :guest{/protocol|}
	
	// resolve and split protocol first
	addrPart := bindings
	protocol := "tcp" // defaults to tcp protocol

	if protoParts := strings.Split(bindings, "/"); len(protoParts) == 2 {
		addrPart = protoParts[0]
		protocol = strings.ToLower(protoParts[1])
	}

	splitCount := strings.Count(addrPart, ":")
	formattedBinding := addrPart
	switch splitCount {
		case 1:
			if addrPart[0] == ':' {
				formattedBinding = "0.0.0.0:0" + formattedBinding
			}
			formattedBinding = "0.0.0.0:" + addrPart
	
		default:
			return "", "", errors.New("Invalid network binding format")
	}
	splitParts := strings.Split(formattedBinding, ":")
	for _, part := range splitParts {
		if len(part) < 1 {
			return "", "", errors.New("Invalid network binding format")
		}
	}

	return formattedBinding, protocol, nil
}

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
			"idle_timeout_seconds": deployTimeout, // TODO: fix why do we have these names so different, they are just gonna create confusion down the reoad
			"ttl_seconds": absoluteDeployTimeout,  // I currently have no idea what ttl seconds mean and what idle timeout seconds mean
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
				sanitizedBindings, protocol, err := sanitizeNetworkBindings(p)
				if err != nil {
					log.Fatalf("invalid port mappings, %e", err)
				}
				
				intface, hport, gport := splitNetworkBindings(sanitizedBindings)
				
				m := map[string]any {
					"host_interface": intface,
					"protocol": protocol,
					"host_port": hport,
					"guest_port": gport,
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
		// Print port mappings so the user knows what was assigned (especially for random `:port` allocation)
		if mappings, ok := result["port_mappings"]; ok && mappings != nil {
			if ms, ok := mappings.([]any); ok && len(ms) > 0 {
				fmt.Println("Port Mappings:")
				for _, m := range ms {
					if mm, ok := m.(map[string]any); ok {
						protocol := mm["protocol"]
						hostPort := mm["host_port"]
						guestPort := mm["guest_port"]
						if mm["host_interface"] != nil && mm["host_interface"] != "0.0.0.0" {
							fmt.Printf("  %v:%v -> VM:%v/%v\n", mm["host_interface"], hostPort, guestPort, protocol)
						} else {
							fmt.Printf("  0.0.0.0:%v -> VM:%v/%v\n", hostPort, guestPort, protocol)
						}
					}
				}
			}
		}
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
