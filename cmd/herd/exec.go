package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/herd-core/herd/internal/config"
	"github.com/herd-core/herd/internal/vsock"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var execCmd = &cobra.Command{
	Use:   "exec [vm-id]",
	Short: "Exec into a running MicroVM",
	Long:  `Connects to a MicroVM's secondary vsock port (5001) and drops you into a raw interactive shell.`,
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		vmID := args[0]
		if err := runExec(vmID); err != nil {
			log.Fatalf("exec failed: %v", err)
		}
	},
}

func init() {
	rootCmd.AddCommand(execCmd)
}

func runExec(vmID string) error {
	// Load config to find the jailer chroot base directory
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config %q: %w", configPath, err)
	}

	// The factory creates sockets in <JailerChrootBaseDir>/firecracker/<vmID>/root/run/<vmID>.sock
	socketPath := filepath.Join(cfg.Jailer.ChrootBaseDir, "firecracker", vmID, "root", "run", fmt.Sprintf("%s.sock", vmID))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	fmt.Printf("Connecting to %s on vsock port 5001...\n", vmID)
	conn, err := vsock.DialFirecracker(ctx, socketPath, 5001)
	if err != nil {
		return fmt.Errorf("failed to dial vsock: %w", err)
	}
	defer func() {
		if cerr := conn.Close(); cerr != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to close vsock connection: %v\n", cerr)
		}
	}()

	// Put the host terminal into raw mode
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("failed to set raw terminal: %w", err)
	}
	defer func() { _ = term.Restore(int(os.Stdin.Fd()), oldState) }()

	errc := make(chan error, 1)

	go func() {
		_, err := io.Copy(os.Stdout, conn)
		errc <- err
	}()

	go func() {
		_, err := io.Copy(conn, os.Stdin)
		errc <- err
	}()

	// Wait for connection to close
	<-errc
	return nil
}
