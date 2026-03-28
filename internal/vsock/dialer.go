package vsock

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"time"
)

// ExecPayload defines the JSON structure sent to the guest-agent to trigger a command.
type ExecPayload struct {
	Command []string `json:"command"`
}

// DialFirecracker connects to a Firecracker microVM vsock port using its Unix Domain Socket.
// Firecracker abstracts vsock devices behind a host-side UDS. To connect to a guest port,
// the host must dial the UDS and perform a plaintext protocol handshake.
func DialFirecracker(ctx context.Context, udsPath string, port uint32) (net.Conn, error) {
	// Use a dialer that respects the provided context (e.g., for timeouts)
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "unix", udsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to dial vsock uds at %s: %w", udsPath, err)
	}

	// Wait, if it fails from this point on, we must clean up the conn
	success := false
	defer func() {
		if !success {
			conn.Close()
		}
	}()

	// 1. Send the handshake command to Firecracker
	// Format: "CONNECT <PORT>\n"
	handshake := fmt.Sprintf("CONNECT %d\n", port)
	
	// Consider writing with a deadline
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetWriteDeadline(deadline)
	} else {
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	}
	
	if _, err := conn.Write([]byte(handshake)); err != nil {
		return nil, fmt.Errorf("failed to send CONNECT handshake: %w", err)
	}

	// 2. Read the response from Firecracker
	// Format: "OK <PORT>\n"
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetReadDeadline(deadline)
	} else {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	}

	reader := bufio.NewReader(conn)
	response, err := reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("failed to read handshake response: %w", err)
	}

	expected := fmt.Sprintf("OK %d\n", port)
	if response != expected {
		return nil, fmt.Errorf("handshake rejected: expected %q, got %q", expected, response)
	}

	// Clear deadlines for the actual payload proxying
	_ = conn.SetDeadline(time.Time{})

	success = true
	return conn, nil
}

// Execute sends the ExecPayload to the guest vsock server over the given connection,
// and streams the resulting output strictly to the provided writers. Since standard Go
// exec.Cmd combines stdout and stderr if we just wire them both to the same net.Conn
// on the guest, we stream everything from the connection into the stdout writer here
// (unless the guest agent multiplexes them with headers, which is out of scope for now).
func Execute(ctx context.Context, conn net.Conn, payload ExecPayload, stdout io.Writer) error {
	defer conn.Close()

	// 1. Serialize and send the instruction payload
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to encode execution payload: %w", err)
	}

	// Null-terminate or newline-terminate so the guest agent knows the payload is done
	data = append(data, '\n')

	if _, err := conn.Write(data); err != nil {
		return fmt.Errorf("failed to transmit payload: %w", err)
	}

	// 2. Stream the execution output back to the host
	errc := make(chan error, 1)
	go func() {
		_, err := io.Copy(stdout, conn)
		errc <- err
	}()

	select {
	case <-ctx.Done():
		return ctx.Err() // conn.Close() will kill the io.Copy
	case err := <-errc:
		if err != nil && err != io.EOF {
			return fmt.Errorf("stream failed: %w", err)
		}
		return nil
	}
}
