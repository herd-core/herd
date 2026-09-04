package network

import (
	"errors"
	"net"
	"net/netip"
)

type PortBinding struct {
	IP        netip.Addr
	HostPort  uint16
	GuestPort uint16
	Protocol  string
}

func GetInterfaceIP(name string) (string, error) {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return "", err
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return "", err
	}
	for _, addr := range addrs {
		var ip net.IP
		switch v := addr.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip == nil || ip.IsLoopback() {
			continue
		}
		ip = ip.To4()
		if ip == nil {
			continue // skip ipv6 for now
		}
		return ip.String(), nil
	}
	return "", errors.New("no suitable IPv4 address found for interface")
}
