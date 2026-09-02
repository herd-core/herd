package network

import (
	"errors"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

type PortBinding struct {
	IP        netip.Addr
	HostPort  uint16
	GuestPort uint16
	Protocol  string
}

func SanitizeNetworkBindings(input string) (PortBinding, error) {
	// setting some default values before hand.
	output := PortBinding{
		IP:        netip.MustParseAddr("0.0.0.0"), // will panic if the string is bad
		HostPort:  0,
		GuestPort: 0,
		Protocol:  "tcp",
	}
	//. okay how do I want this to work?
	// i am gonna have some default values for the struct, then I will parse the string one by one,
	// until I update the struct and then I will just return the struct, using this approach I dont need to do seperate
	// binding split fn call.

	input = strings.ToLower(input)
	parts := strings.Split(input, "/")

	if len(parts) > 2 {
		return output, errors.New("Invalid network binding: More than two / splits")
	} else if len(parts) == 2 {
		validProtocols := []string{"tcp", "udp"}
		// the second one is the protocol, and we need to parse and validate.
		if slices.Contains(validProtocols, parts[1]) {
			output.Protocol = parts[1]
			input = parts[0] // for further processing input string is updated.
		} else {
			return output, errors.New("Invalid Protocol format: Must be one of tcp, udp")
		}
	}

	// now the input should always without '/' splits, and protocols like /tcp and /udp stripped.
	colon_parts := strings.Split(input, ":")
	if len(colon_parts) > 3 || len(colon_parts) == 0 {
		return output, errors.New("Invalid network binding too many colon splits")
	}

	// reverse the list for easier processing in place
	slices.Reverse(colon_parts)

	for i, part := range colon_parts {
		switch i {
		case 0:

			// this is the guest port, try to parse this.
			guestPort, err := strconv.ParseUint(part, 10, 16)
			if err != nil || guestPort > 65535 {
				return output, errors.New("Invalid Guest Port")
			}
			output.GuestPort = uint16(guestPort)

		case 1:
			if part == "" {
				continue // skip parsing, leaves output.HostPort as the default 0
			}
			// this is the host port, try to parse this.
			hostPort, err := strconv.ParseUint(part, 10, 16)
			if err != nil || hostPort > 65535 {
				return output, errors.New("Invalid Host Port")
			}
			output.HostPort = uint16(hostPort)

		case 2:
			if part == "" {
				continue // skip parsing, leaves output.HostPort as the default 0
			}
			// this is the intface IP, lets parse this only restricted to IPv4
			ipv4Addr, err := netip.ParseAddr(part)
			if err != nil || !ipv4Addr.Is4() {
				return output, errors.New("Invalid Interface IP: only support IPv4 interface")
			}
			output.IP = ipv4Addr
		}
	}

	return output, nil
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
