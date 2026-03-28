package network

import (
	"bytes"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
)

// Subnet is the internal IP range used for Firecracker microVMs.
const Subnet = "172.16.0.0/24"

// Bootstrap dynamically configures the host's networking stack to allow
// the MicroVMs to reach the internet via NAT and IP Forwarding.
func Bootstrap() error {
	slog.Info("bootstrapping host nat routing for microVMs", "subnet", Subnet)

	iface, err := getDefaultInterface()
	if err != nil {
		return fmt.Errorf("detect default interface: %w", err)
	}
	slog.Info("detected outbound interface", "iface", iface)

	if err := runCmd("sysctl", "-w", "net.ipv4.ip_forward=1"); err != nil {
		return err
	}

	if err := runCmd("iptables", "-t", "nat", "-A", "POSTROUTING", "-o", iface, "-s", Subnet, "-j", "MASQUERADE"); err != nil {
		return err
	}
	if err := runCmd("iptables", "-A", "FORWARD", "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"); err != nil {
		return err
	}
	if err := runCmd("iptables", "-A", "FORWARD", "-s", Subnet, "-o", iface, "-j", "ACCEPT"); err != nil {
		return err
	}

	slog.Info("nat routing successfully established")
	return nil
}

// Teardown safely reverses the NAT setup.
func Teardown() error {
	slog.Info("tearing down host nat routing", "subnet", Subnet)

	iface, err := getDefaultInterface()
	if err != nil {
		return fmt.Errorf("detect default interface for teardown: %w", err)
	}

	// We intentionally suppress errors on teardown so that the failure to delete one rule doesn't halt the rest.
	_ = runCmd("iptables", "-t", "nat", "-D", "POSTROUTING", "-o", iface, "-s", Subnet, "-j", "MASQUERADE")
	_ = runCmd("iptables", "-D", "FORWARD", "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT")
	_ = runCmd("iptables", "-D", "FORWARD", "-s", Subnet, "-o", iface, "-j", "ACCEPT")

	slog.Info("nat routing successfully torn down")
	return nil
}

// CreateTap creates a new TAP interface and assigns it an IP.
func CreateTap(name, ipAddr string) error {
	slog.Info("creating tap interface", "name", name, "ip", ipAddr)
	if err := runCmd("ip", "tuntap", "add", "dev", name, "mode", "tap"); err != nil {
		return err
	}
	if err := runCmd("ip", "addr", "add", ipAddr, "dev", name); err != nil {
		return runCmd("ip", "link", "del", "dev", name) // cleanup on fail
	}
	if err := runCmd("ip", "link", "set", "dev", name, "up"); err != nil {
		_ = runCmd("ip", "link", "del", "dev", name)
		return err
	}
	return nil
}

// DeleteTap removes a TAP interface.
func DeleteTap(name string) error {
	slog.Info("deleting tap interface", "name", name)
	return runCmd("ip", "link", "del", "dev", name)
}


// getDefaultInterface parses standard Linux 'ip route' to find the physical outbound network card.
func getDefaultInterface() (string, error) {
	cmd := exec.Command("ip", "route")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("ip route failed: %v", string(out))
	}

	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "default") {
			parts := strings.Fields(line)
			for i, p := range parts {
				if p == "dev" && i+1 < len(parts) {
					return parts[i+1], nil
				}
			}
		}
	}
	return "", fmt.Errorf("no default route interface found in routing tables")
}

func runCmd(name string, args ...string) error {
	slog.Debug("executing command", "cmd", name, "args", args)
	cmd := exec.Command(name, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("cmd %s %v failed: %w, out: %s", name, args, err, bytes.TrimSpace(out))
	}
	return nil
}
