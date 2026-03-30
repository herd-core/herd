package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/herd-core/herd/internal/config"
	"github.com/herd-core/herd/internal/network"
	"github.com/herd-core/herd/internal/storage"
	"github.com/herd-core/herd/internal/system"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)


var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize herd with interactive setup",
	Long:  `Guides you through the process of setting up herd, including kernel selection, resource limits, and environment bootstrapping.`,
	Run: func(cmd *cobra.Command, args []string) {
		runInit()
	},
}

func init() {
	rootCmd.AddCommand(initCmd)
}

func runInit() {
	if os.Geteuid() != 0 {
		log.Fatal("herd init must be run as root (sudo)")
	}

	fmt.Printf("🚀 Welcome to herd initialization (version %s)!\n", config.Version)
	fmt.Println("This process will help you configure herd and set up the required environment.")
	fmt.Println()
	homeDir, err := config.GetTargetHomeDir()
	if err != nil {
		log.Fatalf("failed to determine home directory: %v", err)
	}
	herdDir := filepath.Join(homeDir, ".herd")
	binDir := filepath.Join(herdDir, "bin")
	stateDir := filepath.Join(herdDir, "state")

	fmt.Printf("--- Path Configuration ---\n")
	fmt.Printf("Base directory: %s\n", herdDir)
	reader := bufio.NewReader(os.Stdin)
	if err := os.MkdirAll(binDir, 0755); err != nil {
		log.Fatalf("failed to create bin dir: %v", err)
	}
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		log.Fatalf("failed to create state dir: %v", err)
	}

	// 2. Binaries
	var fcPath string
	useExistingFC := promptConfirm(reader, "Do you already have Firecracker installed? (y/n)", false)
	if useExistingFC {
		fcPath = promptString(reader, "Path to firecracker binary (default: /usr/local/bin/firecracker)", "/usr/local/bin/firecracker")
	} else {
		fcPath = filepath.Join(binDir, "firecracker")
		fmt.Println("Downloading Firecracker v1.14.3...")
		if err := downloadFirecracker(fcPath); err != nil {
			log.Fatalf("failed to download firecracker: %v", err)
		}
		fmt.Printf("✅ Firecracker installed: %s\n", fcPath)
	}

	// Guest Agent - Download pre-compiled binary
	agentPath := filepath.Join(binDir, "herd-guest-agent")
	fmt.Println("Downloading pre-compiled herd-guest-agent...")
	if err := downloadGuestAgent(agentPath); err != nil {
		log.Fatalf("failed to download guest agent: %v", err)
	}
	fmt.Printf("✅ Download complete: %s\n", agentPath)

	// 3. Kernel
	var kernelPath string
	useExistingKernel := promptConfirm(reader, "Do you have your own Linux kernel image for Firecracker? (y/n)", false)
	if useExistingKernel {
		kernelPath = promptString(reader, "Enter path to kernel image (e.g., /path/to/vmlinux)", "")
	} else {
		kernelPath = filepath.Join(stateDir, "vmlinux-v6.1.bin")
		fmt.Printf("Downloading pre-compiled kernel to %s...\n", kernelPath)
		if err := downloadKernel(kernelPath); err != nil {
			log.Fatalf("failed to download kernel: %v", err)
		}
	}
	fmt.Println()

	// 4. Resource Limits
	sysInfo, err := system.GetInfo()
	if err != nil {
		log.Fatalf("failed to get system info: %v", err)
	}

	defaultMaxVMs := sysInfo.CPUCores * 4
	defaultMaxMem := int64(float64(sysInfo.TotalMemoryMB) * 0.8)
	defaultCPUCores := float64(sysInfo.CPUCores) - 1
	if defaultCPUCores < 1 {
		defaultCPUCores = 1
	}

	fmt.Printf("--- Resource Configuration ---\n")
	maxVMs := promptInt(reader, fmt.Sprintf("Max global VMs (default: %d)", defaultMaxVMs), defaultMaxVMs)
	maxMem := promptInt64(reader, fmt.Sprintf("Max global memory MB (default: %d)", defaultMaxMem), defaultMaxMem)
	cpuCores := promptFloat(reader, fmt.Sprintf("CPU limit cores (default: %.1f)", defaultCPUCores), defaultCPUCores)
	fmt.Println()

	// 5. Config Generation
	cfg := config.Config{
		Network: config.NetworkConfig{
			ControlBind: "127.0.0.1:8081",
			DataBind:    "127.0.0.1:8080",
		},
		Storage: config.StorageConfig{
			StateDir:        stateDir,
			SnapshotterName: "devmapper",
			Namespace:       "herd",
		},
		Resources: config.ResourceConfig{
			MaxGlobalVMs:      maxVMs,
			MaxGlobalMemoryMB: maxMem,
			CPULimitCores:     cpuCores,
		},
		Binaries: config.BinaryConfig{
			FirecrackerPath: fcPath,
			KernelImagePath: kernelPath,
			GuestAgentPath:  agentPath,
		},
		Telemetry: config.TelemetryConfig{
			LogFormat:   "json",
			MetricsPath: "/metrics",
		},
	}

	configFilePath := filepath.Join(herdDir, "herd.yaml")
	data, err := yaml.Marshal(&cfg)
	if err != nil {
		log.Fatalf("failed to marshal config: %v", err)
	}
	if err := os.WriteFile(configFilePath, data, 0644); err != nil {
		log.Fatalf("failed to write config file: %v", err)
	}
	fmt.Printf("✅ Configuration saved to %s\n", configFilePath)

	// 6. Bootstrap
	fmt.Println("--- Bootstrapping Environment ---")
	if err := storage.Bootstrap(cfg.Storage.StateDir); err != nil {
		log.Fatalf("failed to bootstrap storage: %v", err)
	}
	if err := network.Bootstrap(); err != nil {
		log.Fatalf("failed to bootstrap network: %v", err)
	}

	fmt.Println("\n🎉 herd initialization completed successfully!")
	fmt.Println("You can now start the daemon with: sudo ./herd start --config " + configFilePath)
}

func downloadFirecracker(outputPath string) error {
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x86_64"
	} else if arch == "arm64" {
		arch = "aarch64"
	}

	version := "1.14.3"
	url := fmt.Sprintf("https://github.com/firecracker-microvm/firecracker/releases/download/v%s/firecracker-v%s-%s.tgz", version, version, arch)

	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download firecracker: %s", resp.Status)
	}

	// Extract .tgz
	gzr, err := gzip.NewReader(resp.Body)
	if err != nil {
		return err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		// The binary in the tar is usually named "firecracker-v1.14.3-x86_64"
		if strings.Contains(header.Name, "firecracker-v") && !strings.Contains(header.Name, ".debug") {
			out, err := os.Create(outputPath)
			if err != nil {
				return err
			}
			defer out.Close()

			if _, err := io.Copy(out, tr); err != nil {
				return err
			}
			return os.Chmod(outputPath, 0755)
		}
	}

	return fmt.Errorf("firecracker binary not found in archive")
}

func downloadGuestAgent(path string) error {
	// Pull from a specific release instead of main for stability
	url := fmt.Sprintf("https://github.com/herd-core/herd/releases/download/%s/herd-guest-agent-linux-%s", config.Version, runtime.GOARCH)

	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download guest agent for %s: %s", runtime.GOARCH, resp.Status)
	}

	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, resp.Body); err != nil {
		return err
	}

	// Make it executable
	return os.Chmod(path, 0755)
}

func promptString(reader *bufio.Reader, label, defaultValue string) string {
	fmt.Printf("%s: ", label)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)
	if input == "" {
		return defaultValue
	}
	return input
}

func promptInt(reader *bufio.Reader, label string, defaultValue int) int {
	for {
		fmt.Printf("%s: ", label)
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)
		if input == "" {
			return defaultValue
		}
		var val int
		if _, err := fmt.Sscanf(input, "%d", &val); err == nil {
			return val
		}
		fmt.Println("Invalid input, please enter a number.")
	}
}

func promptInt64(reader *bufio.Reader, label string, defaultValue int64) int64 {
	for {
		fmt.Printf("%s: ", label)
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)
		if input == "" {
			return defaultValue
		}
		var val int64
		if _, err := fmt.Sscanf(input, "%d", &val); err == nil {
			return val
		}
		fmt.Println("Invalid input, please enter a number.")
	}
}

func promptFloat(reader *bufio.Reader, label string, defaultValue float64) float64 {
	for {
		fmt.Printf("%s: ", label)
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)
		if input == "" {
			return defaultValue
		}
		var val float64
		if _, err := fmt.Sscanf(input, "%f", &val); err == nil {
			return val
		}
		fmt.Println("Invalid input, please enter a decimal number.")
	}
}

func promptConfirm(reader *bufio.Reader, label string, defaultValue bool) bool {
	for {
		fmt.Printf("%s: ", label)
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(strings.ToLower(input))
		if input == "" {
			return defaultValue
		}
		if input == "y" || input == "yes" {
			return true
		}
		if input == "n" || input == "no" {
			return false
		}
		fmt.Println("Please enter 'y' or 'n'.")
	}
}

func downloadKernel(path string) error {
	// Pull from a specific release instead of main for stability
	url := fmt.Sprintf("https://github.com/herd-core/herd/releases/download/%s/vmlinux-v6.1.bin", config.Version)
	
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad status: %s", resp.Status)
	}

	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}
