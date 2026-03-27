package storage

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Bootstrap devmapper and containerd requirements given a working dir.
func Bootstrap(stateDir string) error {
	slog.Info("bootstrapping self-contained storage", "stateDir", stateDir)

	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return fmt.Errorf("failed to create state dir: %w", err)
	}

	devMapperDir := filepath.Join(stateDir, "devmapper")
	if err := os.MkdirAll(devMapperDir, 0755); err != nil {
		return fmt.Errorf("failed to create devmapper dir: %w", err)
	}

	dataFilePath := filepath.Join(devMapperDir, "data")
	metaFilePath := filepath.Join(devMapperDir, "metadata")

	// 1. Create sparse files
	if err := createSparseFile(dataFilePath, 2*1024*1024*1024); err != nil { // 2GB
		return err
	}
	if err := createSparseFile(metaFilePath, 1*1024*1024*1024); err != nil { // 1GB
		return err
	}

	// 2. Bind loop devices
	dataLoop, err := setupLoopDevice(dataFilePath)
	if err != nil {
		return err
	}
	metaLoop, err := setupLoopDevice(metaFilePath)
	if err != nil {
		return err
	}

	poolName := "herd-thinpool"

	// 3. Create thin-pool via dmsetup
	if !thinPoolExists(poolName) {
		slog.Info("creating devicemapper thin-pool", "pool", poolName)

		// Get data device size in 512-byte sectors
		dataSize, err := getBlockdevSize(dataLoop)
		if err != nil {
			return err
		}
		
		sectorSize := 512
		lengthSectors := dataSize / int64(sectorSize)
		dataBlockSize := 128
		lowWaterMark := 32768
		
		table := fmt.Sprintf("0 %d thin-pool %s %s %d %d 1 skip_block_zeroing", lengthSectors, metaLoop, dataLoop, dataBlockSize, lowWaterMark)

		cmd := exec.Command("dmsetup", "create", poolName, "--table", table)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to create thin-pool: %s (%w)", string(out), err)
		}
	} else {
		// Attempt to reload table if it already exists to ensure loop devices map correctly
		_ = exec.Command("dmsetup", "reload", poolName).Run()
	}
	
	// 4. Generate containerd configs
	ctrdDir := filepath.Join(stateDir, "containerd")
	os.MkdirAll(ctrdDir, 0755)
	
	configPath := filepath.Join(stateDir, "config.toml")
	sockPath := filepath.Join(stateDir, "containerd.sock")
	
	ctrdConfig := fmt.Sprintf(`
version = 2
root = "%s"
state = "%s"

[grpc]
  address = "%s"

[plugins]
  [plugins."io.containerd.snapshotter.v1.devmapper"]
    pool_name = "%s"
    root_path = "%s"
    base_image_size = "10GB"
    discard_blocks = true
`, filepath.Join(ctrdDir, "root"), filepath.Join(ctrdDir, "state"), sockPath, poolName, devMapperDir)

	if err := os.WriteFile(configPath, []byte(ctrdConfig), 0644); err != nil {
		return fmt.Errorf("failed to write containerd config: %w", err)
	}

	return nil
}

func createSparseFile(path string, size int64) error {
	if _, err := os.Stat(path); err == nil {
		return nil // File already exists
	}

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed creating sparse file %s: %w", path, err)
	}
	defer f.Close()

	if err := f.Truncate(size); err != nil {
		return fmt.Errorf("failed to truncate file %s: %w", path, err)
	}

	return nil
}

func setupLoopDevice(filePath string) (string, error) {
	// Check if already bound
	cmd := exec.Command("losetup", "--output", "NAME", "--noheadings", "--associated", filePath)
	out, err := cmd.Output()
	if err == nil {
		dev := strings.TrimSpace(string(out))
		if dev != "" {
			return dev, nil
		}
	}

	// Create new loop device
	cmd = exec.Command("losetup", "--find", "--show", filePath)
	out, err = cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to map loop device for %s: %s (%w)", filePath, string(out), err)
	}

	return strings.TrimSpace(string(out)), nil
}

func thinPoolExists(name string) bool {
	cmd := exec.Command("dmsetup", "status", name)
	return cmd.Run() == nil
}

func getBlockdevSize(dev string) (int64, error) {
	cmd := exec.Command("blockdev", "--getsize64", dev)
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("failed to get size for %s: %w", dev, err)
	}
	
	sizeStr := strings.TrimSpace(string(out))
	var size int64
	_, err = fmt.Sscanf(sizeStr, "%d", &size)
	return size, err
}
