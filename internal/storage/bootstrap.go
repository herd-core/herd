package storage

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Bootstrap devmapper and containerd requirements given a working dir.
func Bootstrap(stateDir string) (err error) {
	slog.Info("bootstrapping self-contained storage", "stateDir", stateDir)

	cleanupStack := make([]func(), 0, 8)
	defer func() {
		if err == nil {
			return
		}

		slog.Warn("bootstrap failed; running rollback", "error", err)
		for i := len(cleanupStack) - 1; i >= 0; i-- {
			cleanupStack[i]()
		}
	}()

	stateDirInfo, statErr := os.Stat(stateDir)
	stateDirPreExisted := statErr == nil && stateDirInfo.IsDir()

	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return fmt.Errorf("failed to create state dir: %w", err)
	}
	if !stateDirPreExisted {
		cleanupStack = append(cleanupStack, func() {
			if rmErr := os.RemoveAll(stateDir); rmErr != nil {
				slog.Warn("rollback failed to remove state directory", "path", stateDir, "error", rmErr)
			}
		})
	}

	devMapperDir := filepath.Join(stateDir, "devmapper")
	devMapperInfo, devMapperStatErr := os.Stat(devMapperDir)
	devMapperDirPreExisted := devMapperStatErr == nil && devMapperInfo.IsDir()

	if err := os.MkdirAll(devMapperDir, 0755); err != nil {
		return fmt.Errorf("failed to create devmapper dir: %w", err)
	}
	if !devMapperDirPreExisted {
		cleanupStack = append(cleanupStack, func() {
			if rmErr := os.RemoveAll(devMapperDir); rmErr != nil {
				slog.Warn("rollback failed to remove devmapper directory", "path", devMapperDir, "error", rmErr)
			}
		})
	}

	dataFilePath := filepath.Join(devMapperDir, "data")
	metaFilePath := filepath.Join(devMapperDir, "metadata")

	// 1. Create sparse files
	createdDataFile, err := createSparseFile(dataFilePath, 20*1024*1024*1024)
	if err != nil { 
		return err
	}
	if createdDataFile {
		cleanupStack = append(cleanupStack, func() {
			if rmErr := os.Remove(dataFilePath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
				slog.Warn("rollback failed to remove data sparse file", "path", dataFilePath, "error", rmErr)
			}
		})
	}

	createdMetaFile, err := createSparseFile(metaFilePath, 2*1024*1024*1024)
	if err != nil {
		return err
	}
	if createdMetaFile {
		cleanupStack = append(cleanupStack, func() {
			if rmErr := os.Remove(metaFilePath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
				slog.Warn("rollback failed to remove metadata sparse file", "path", metaFilePath, "error", rmErr)
			}
		})
	}

	// 2. Bind loop devices
	dataLoop, createdDataLoop, err := setupLoopDevice(dataFilePath)
	if err != nil {
		return err
	}
	if createdDataLoop {
		cleanupStack = append(cleanupStack, func() {
			if detachErr := detachLoopDevice(dataLoop); detachErr != nil {
				slog.Warn("rollback failed to detach data loop", "loop", dataLoop, "error", detachErr)
			}
		})
	}

	metaLoop, createdMetaLoop, err := setupLoopDevice(metaFilePath)
	if err != nil {
		return err
	}
	if createdMetaLoop {
		cleanupStack = append(cleanupStack, func() {
			if detachErr := detachLoopDevice(metaLoop); detachErr != nil {
				slog.Warn("rollback failed to detach metadata loop", "loop", metaLoop, "error", detachErr)
			}
		})
	}

	poolName := "herd-thinpool"

	// 3. Create thin-pool via dmsetup
	createdThinPool := false
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
		createdThinPool = true
	} else {
		// Attempt to reload table if it already exists to ensure loop devices map correctly
		_ = exec.Command("dmsetup", "reload", poolName).Run()
	}
	if createdThinPool {
		cleanupStack = append(cleanupStack, func() {
			if removeErr := removeThinPool(poolName); removeErr != nil {
				slog.Warn("rollback failed to remove thin-pool", "pool", poolName, "error", removeErr)
			}
		})
	}

	// 4. Generate containerd configs
	ctrdDir := filepath.Join(stateDir, "containerd")
	ctrdInfo, ctrdStatErr := os.Stat(ctrdDir)
	ctrdDirPreExisted := ctrdStatErr == nil && ctrdInfo.IsDir()
	if err := os.MkdirAll(ctrdDir, 0755); err != nil {
		return fmt.Errorf("failed to create containerd directory: %w", err)
	}
	if !ctrdDirPreExisted {
		cleanupStack = append(cleanupStack, func() {
			if rmErr := os.RemoveAll(ctrdDir); rmErr != nil {
				slog.Warn("rollback failed to remove containerd directory", "path", ctrdDir, "error", rmErr)
			}
		})
	}

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
    base_image_size = "5GB"
    discard_blocks = true
`, filepath.Join(ctrdDir, "root"), filepath.Join(ctrdDir, "state"), sockPath, poolName, devMapperDir)

	if err := os.WriteFile(configPath, []byte(ctrdConfig), 0644); err != nil {
		return fmt.Errorf("failed to write containerd config: %w", err)
	}
	cleanupStack = append(cleanupStack, func() {
		if rmErr := os.Remove(configPath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			slog.Warn("rollback failed to remove containerd config", "path", configPath, "error", rmErr)
		}
	})

	// Start containerd in the background
	slog.Info("starting containerd", "config", configPath)
	cmd := exec.Command("containerd", "--config", configPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // detach
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start containerd: %w", err)
	}

	// Wait for the socket to be created
	slog.Info("waiting for containerd socket", "socket", sockPath)
	for i := 0; i < 30; i++ {
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	return nil
}

// Teardown intentionally destroys previously bootstrapped devmapper/containerd state in strict reverse dependency order.
func Teardown(stateDir string) error {
	slog.Info("tearing down self-contained storage", "stateDir", stateDir)

	var teardownErrs []error

	configPath := filepath.Join(stateDir, "config.toml")
	pkillPattern := fmt.Sprintf("containerd --config %s", configPath)
	pkillCmd := exec.Command("pkill", "-f", pkillPattern)
	if out, err := pkillCmd.CombinedOutput(); err != nil {
		exitCode := -1
		if pkillCmd.ProcessState != nil {
			exitCode = pkillCmd.ProcessState.ExitCode()
		}
		if exitCode != 1 {
			teardownErrs = append(teardownErrs, fmt.Errorf("failed to stop containerd: %s (%w)", strings.TrimSpace(string(out)), err))
		}
	}

	time.Sleep(time.Second)

	if err := removeThinPoolWithRetry("herd-thinpool", 5, time.Second); err != nil {
		teardownErrs = append(teardownErrs, err)
	}

	dataFilePath := filepath.Join(stateDir, "devmapper", "data")
	metaFilePath := filepath.Join(stateDir, "devmapper", "metadata")
	for _, filePath := range []string{dataFilePath, metaFilePath} {
		loopDevices, err := loopDevicesForFile(filePath)
		if err != nil {
			teardownErrs = append(teardownErrs, err)
			continue
		}

		for _, loopDev := range loopDevices {
			if err := detachLoopDevice(loopDev); err != nil {
				teardownErrs = append(teardownErrs, fmt.Errorf("failed detaching loop %s for %s: %w", loopDev, filePath, err))
			}
		}
	}

	if err := os.RemoveAll(stateDir); err != nil {
		teardownErrs = append(teardownErrs, fmt.Errorf("failed to remove state dir %s: %w", stateDir, err))
	}

	return errors.Join(teardownErrs...)
}

func createSparseFile(path string, size int64) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil // File already exists
	}

	f, err := os.Create(path)
	if err != nil {
		return false, fmt.Errorf("failed creating sparse file %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	if err := f.Truncate(size); err != nil {
		return false, fmt.Errorf("failed to truncate file %s: %w", path, err)
	}

	return true, nil
}

func setupLoopDevice(filePath string) (string, bool, error) {
	// Check if already bound
	cmd := exec.Command("losetup", "--output", "NAME", "--noheadings", "--associated", filePath)
	out, err := cmd.Output()
	if err == nil {
		dev := strings.TrimSpace(string(out))
		if dev != "" {
			return dev, false, nil
		}
	}

	// Create new loop device
	cmd = exec.Command("losetup", "--find", "--show", filePath)
	out, err = cmd.CombinedOutput()
	if err != nil {
		return "", false, fmt.Errorf("failed to map loop device for %s: %s (%w)", filePath, string(out), err)
	}

	return strings.TrimSpace(string(out)), true, nil
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

func removeThinPool(name string) error {
	cmd := exec.Command("dmsetup", "remove", name)
	out, err := cmd.CombinedOutput()
	if err != nil {
		trimmed := strings.TrimSpace(string(out))
		if strings.Contains(trimmed, "No such device") || strings.Contains(trimmed, "not found") {
			return nil
		}
		return fmt.Errorf("failed to remove thin-pool %s: %s (%w)", name, trimmed, err)
	}
	return nil
}

func removeThinPoolWithRetry(name string, attempts int, delay time.Duration) error {
	var lastErr error
	for i := 0; i < attempts; i++ {
		err := removeThinPool(name)
		if err == nil {
			return nil
		}
		lastErr = err
		time.Sleep(delay)
	}
	return fmt.Errorf("thin-pool remove retries exhausted for %s: %w", name, lastErr)
}

func loopDevicesForFile(filePath string) ([]string, error) {
	cmd := exec.Command("losetup", "-j", filePath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		trimmed := strings.TrimSpace(string(out))
		if trimmed == "" {
			return nil, nil
		}
		return nil, fmt.Errorf("failed listing loop devices for %s: %s (%w)", filePath, trimmed, err)
	}

	text := strings.TrimSpace(string(out))
	if text == "" {
		return nil, nil
	}

	lines := strings.Split(text, "\n")
	loopDevices := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) > 0 {
			loopDev := strings.TrimSpace(parts[0])
			if loopDev != "" {
				loopDevices = append(loopDevices, loopDev)
			}
		}
	}

	return loopDevices, nil
}

func detachLoopDevice(loopDevice string) error {
	cmd := exec.Command("losetup", "-d", loopDevice)
	out, err := cmd.CombinedOutput()
	if err != nil {
		trimmed := strings.TrimSpace(string(out))
		if strings.Contains(trimmed, "No such device") {
			return nil
		}
		return fmt.Errorf("failed to detach loop device %s: %s (%w)", loopDevice, trimmed, err)
	}
	return nil
}
