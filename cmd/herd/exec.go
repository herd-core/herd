package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

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
	// The factory creates sockets in /tmp/<vm-id>.sock
	socketPath := filepath.Join("/tmp", fmt.Sprintf("%s.sock", vmID))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	fmt.Printf("Connecting to %s on vsock port 5001...\n", vmID)
	conn, err := vsock.DialFirecracker(ctx, socketPath, 5001)
	if err != nil {
		return fmt.Errorf("failed to dial vsock: %w", err)
	}
	defer conn.Close()

	// Put the host terminal into raw mode
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("failed to set raw terminal: %w", err)
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

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
