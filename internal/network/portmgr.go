package network

import (
	"fmt"
	"net"
	"sync"
)

// PortManager tracks host port usage across all MicroVMs managed by the daemon.
// It supports both explicit "deterministic" allocation and random "ephemeral" allocation.
type PortManager struct {
	mu             sync.Mutex
	inUse          map[int]string // port -> vmID
	ephemeralStart int
	ephemeralEnd   int
}

// NewPortManager initializes a new PortManager with the specified ephemeral range.
func NewPortManager(start, end int) *PortManager {
	return &PortManager{
		inUse:          make(map[int]string),
		ephemeralStart: start,
		ephemeralEnd:   end,
	}
}

// Allocate attempts to reserve a port for a specific VM.
// If requestedPort is 0, it picks an available port from the ephemeral pool.
// If requestedPort is > 0, it checks if the port is available and claims it.
func (pm *PortManager) Allocate(requestedPort int, protocol string, iface string, vmID string) (int, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if requestedPort == 0 {
		// Find first available in ephemeral range
		for p := pm.ephemeralStart; p <= pm.ephemeralEnd; p++ {
			if _, exists := pm.inUse[p]; !exists {
				// Also check if host actually allows binding (ephemeral: any interface)
				if pm.isHostPortFree(protocol, p, "") {
					pm.inUse[p] = vmID
					return p, nil
				}
			}
		}
		return 0, fmt.Errorf("no ephemeral ports available in range %d-%d", pm.ephemeralStart, pm.ephemeralEnd)
	}

	// Explicit request
	if owner, exists := pm.inUse[requestedPort]; exists {
		if owner == vmID {
			return requestedPort, nil // Already owned by this VM
		}
		return 0, fmt.Errorf("port %d already in use by VM %s within herd", requestedPort, owner)
	}

	// Check if something else on the host is using it (on the specific interface)
	if !pm.isHostPortFree(protocol, requestedPort, iface) {
		return 0, fmt.Errorf("port %d is already in use by another process on the host", requestedPort)
	}

	pm.inUse[requestedPort] = vmID
	return requestedPort, nil
}

func (pm *PortManager) isHostPortFree(protocol string, port int, iface string) bool {
	// Use the specific interface if provided, otherwise check all interfaces.
	host := "0.0.0.0"
	if iface != "" && iface != "0.0.0.0" {
		host = iface
	}
	addr := fmt.Sprintf("%s:%d", host, port)
	if protocol == "udp" {
		l, err := net.ListenPacket("udp", addr)
		if err != nil {
			return false
		}
		l.Close()
	} else {
		l, err := net.Listen("tcp", addr)
		if err != nil {
			return false
		}
		l.Close()
	}
	return true
}

// Release frees a previously allocated port.
func (pm *PortManager) Release(port int) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	delete(pm.inUse, port)
}
