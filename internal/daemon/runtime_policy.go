package daemon

import (
	"fmt"
	"log"
)

// EnforceRuntimePolicy validates platform support and emits guarantee warnings.
// Linux runs with full guarantees. macOS is supported in reduced-guarantee mode.
// Other operating systems fail fast at startup.
func EnforceRuntimePolicy(goos string, logger *log.Logger) error {
	switch goos {
	case "linux":
		logger.Println("runtime: linux detected; full worker parent-death guarantees enabled")
		return nil
	case "darwin":
		logger.Println("WARNING: runtime: macOS detected; reduced guarantees mode enabled")
		logger.Println("WARNING: runtime: kernel-level parent-death SIGKILL is unavailable on macOS")
		logger.Println("WARNING: runtime: daemon crash may leave orphaned child processes; cleanup is best effort")
		return nil
	default:
		return fmt.Errorf("unsupported platform %q: herd daemon supports linux (full guarantees) and macOS (reduced guarantees)", goos)
	}
}
