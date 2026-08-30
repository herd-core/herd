package network

import (
	"fmt"
	"strings"
	"strconv"
	"errors"
)

func SplitNetworkBindings(bindings string) (string, int8, int8) {
	// we assume perfect bindings of the format interface:host_port:guest_port
	parts := strings.Split(bindings, ":")
	intface := parts[0]
	hostPort, err := strconv.ParseInt(parts[1], 10, 8)
	if err != nil {
		fmt.Println("Unable to parse host network bindings")
	}
	guestPort, err := strconv.ParseInt(parts[2], 10, 8)
	if err != nil {
		fmt.Println("Unable to parse guest network bindings")
	}
	return intface, int8(hostPort), int8(guestPort)
}

func SanitizeNetworkBindings(bindings string) (string, string, error) {

	// could be of the format
	// int:host:guest{/protocol|}
	// host:guest{/protocol|}
	// :guest{/protocol|}

	// resolve and split protocol first
	addrPart := bindings
	protocol := "tcp" // defaults to tcp protocol

	if protoParts := strings.Split(bindings, "/"); len(protoParts) == 2 {
		addrPart = protoParts[0]
		protocol = strings.ToLower(protoParts[1])
	}

	splitCount := strings.Count(addrPart, ":")
	formattedBinding := addrPart
	switch splitCount {
	case 1:
		if addrPart[0] == ':' {
			formattedBinding = "0" + formattedBinding
		} 
		formattedBinding = "0.0.0.0:" + formattedBinding
	default:
		return "", "", errors.New("Invalid network binding format")
	}
	splitParts := strings.Split(formattedBinding, ":")
	for _, part := range splitParts {
		if len(part) < 1 {
			return "", "", errors.New("Invalid network binding format")
		}
	}

	return formattedBinding, protocol, nil
}