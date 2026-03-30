package system

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// Info holds host system resource information.
type Info struct {
	CPUCores     int
	TotalMemoryMB int64
}

// GetInfo retrieves CPU and Memory information from the host.
func GetInfo() (*Info, error) {
	info := &Info{
		CPUCores: runtime.NumCPU(),
	}

	memMB, err := getTotalMemoryMB()
	if err != nil {
		return nil, err
	}
	info.TotalMemoryMB = memMB

	return info, nil
}

func getTotalMemoryMB() (int64, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "MemTotal:") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				val, err := strconv.ParseInt(parts[1], 10, 64)
				if err != nil {
					return 0, err
				}
				return val / 1024, nil // kB to MB
			}
		}
	}

	return 0, nil
}
