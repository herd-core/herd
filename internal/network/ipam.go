package network

import (
	"fmt"
	"net"
	"sync"
)

// IPAM manages the allocation of /32 point-to-point IP addresses
// for Firecracker MicroVMs out of a given CIDR block.
type IPAM struct {
	mu      sync.Mutex
	subnet  *net.IPNet
	usedIPs map[string]bool
	nextIP  net.IP
}

// NewIPAM creates a new IP address manager from a CIDR string.
func NewIPAM(cidr string) (*IPAM, error) {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("invalid cidr: %w", err)
	}

	// clone starting IP, skip .0 (network id) and .1 (host gateway)
	start := make(net.IP, len(ipnet.IP))
	copy(start, ipnet.IP)
	inc(start) // .1
	inc(start) // .2

	return &IPAM{
		subnet:  ipnet,
		usedIPs: make(map[string]bool),
		nextIP:  start,
	}, nil
}

// Acquire gets an unused /32 guest IP from the configured subnet block.
func (i *IPAM) Acquire() (string, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	startIP := make(net.IP, len(i.nextIP))
	copy(startIP, i.nextIP)

	for {
		ipStr := i.nextIP.String()
		if !i.usedIPs[ipStr] {
			i.usedIPs[ipStr] = true
			res := ipStr
			inc(i.nextIP)
			if !i.subnet.Contains(i.nextIP) || isBroadcast(i.nextIP, i.subnet) {
				// wrap around to the beginning (.2)
				copy(i.nextIP, i.subnet.IP)
				inc(i.nextIP)
				inc(i.nextIP)
			}
			return res, nil
		}

		inc(i.nextIP)
		if !i.subnet.Contains(i.nextIP) || isBroadcast(i.nextIP, i.subnet) {
			copy(i.nextIP, i.subnet.IP)
			inc(i.nextIP)
			inc(i.nextIP)
		}

		// if we wrapped around completely, it's exhausted.
		if i.nextIP.Equal(startIP) {
			return "", fmt.Errorf("ip pool %s exhausted", i.subnet.String())
		}
	}
}

// Release returns the IP to the pool.
func (i *IPAM) Release(ip string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	delete(i.usedIPs, ip)
}

func inc(ip net.IP) {
	for j := len(ip) - 1; j >= 0; j-- {
		ip[j]++
		if ip[j] > 0 {
			break
		}
	}
}

func isBroadcast(ip net.IP, n *net.IPNet) bool {
	if len(ip) != len(n.Mask) {
		return false
	}
	for i := range ip {
		if (ip[i] | n.Mask[i]) != 255 {
			return false
		}
	}
	return true
}
