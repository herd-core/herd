package network

import (
	"bytes"
	"fmt"
	"log/slog"
	"net"
	"os/exec"
	"strconv"

	"github.com/vishvananda/netlink"
)

// Subnet is the internal IP range used for Firecracker microVMs.
const Subnet = "10.200.0.0/16"

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

	// Drop all traffic from the MicroVM subnet to RFC 1918 private subnets for isolation
	for _, privNet := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"} {
		if err := runCmd("iptables", "-A", "FORWARD", "-s", Subnet, "-d", privNet, "-j", "DROP"); err != nil {
			return err
		}
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

	// Remove RFC 1918 drop rules
	for _, privNet := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"} {
		_ = runCmd("iptables", "-D", "FORWARD", "-s", Subnet, "-d", privNet, "-j", "DROP")
	}

	_ = runCmd("iptables", "-D", "FORWARD", "-s", Subnet, "-o", iface, "-j", "ACCEPT")

	slog.Info("nat routing successfully torn down")
	return nil
}

// CreateTap creates a new TAP interface and assigns it an IP.
func CreateTap(name, ipAddr string) error {
	slog.Info("creating tap interface", "name", name, "ip", ipAddr)

	tap := &netlink.Tuntap{
		LinkAttrs: netlink.LinkAttrs{Name: name},
		Mode:      netlink.TUNTAP_MODE_TAP,
	}
	if err := netlink.LinkAdd(tap); err != nil {
		return fmt.Errorf("netlink: add tap %s: %w", name, err)
	}

	link, err := netlink.LinkByName(name)
	if err != nil {
		return fmt.Errorf("netlink: find tap %s after creation: %w", name, err)
	}

	addr, err := netlink.ParseAddr(ipAddr)
	if err != nil {
		_ = netlink.LinkDel(link)
		return fmt.Errorf("netlink: parse addr %q: %w", ipAddr, err)
	}
	if err := netlink.AddrAdd(link, addr); err != nil {
		_ = netlink.LinkDel(link)
		return fmt.Errorf("netlink: addr add %s on %s: %w", ipAddr, name, err)
	}

	if err := netlink.LinkSetUp(link); err != nil {
		_ = netlink.LinkDel(link)
		return fmt.Errorf("netlink: set %s up: %w", name, err)
	}

	return nil
}

// CreatePointToPointTap creates a new TAP interface and establishes a Point-to-Point peer route.
// Uses netlink syscalls directly to avoid fork/exec overhead and kernel RTNL lock contention
// from concurrent `ip` command invocations.
func CreatePointToPointTap(name, hostIP, guestIP string, uid, gid int) error {
	slog.Info("creating p2p tap interface", "name", name, "host", hostIP, "guest", guestIP)

	tap := &netlink.Tuntap{
		LinkAttrs: netlink.LinkAttrs{Name: name},
		Mode:      netlink.TUNTAP_MODE_TAP,
		Owner:     uint32(uid),
		Group:     uint32(gid),
	}
	if err := netlink.LinkAdd(tap); err != nil {
		return fmt.Errorf("netlink: add tap %s: %w", name, err)
	}

	link, err := netlink.LinkByName(name)
	if err != nil {
		return fmt.Errorf("netlink: find tap %s after creation: %w", name, err)
	}

	host := net.ParseIP(hostIP)
	peer := net.ParseIP(guestIP)
	if host == nil || peer == nil {
		_ = netlink.LinkDel(link)
		return fmt.Errorf("netlink: invalid IPs host=%q peer=%q", hostIP, guestIP)
	}

	addr := &netlink.Addr{
		IPNet: &net.IPNet{IP: host, Mask: net.CIDRMask(32, 32)},
		Peer:  &net.IPNet{IP: peer, Mask: net.CIDRMask(32, 32)},
	}
	if err := netlink.AddrAdd(link, addr); err != nil {
		_ = netlink.LinkDel(link)
		return fmt.Errorf("netlink: addr add %s peer %s on %s: %w", hostIP, guestIP, name, err)
	}

	if err := netlink.LinkSetUp(link); err != nil {
		_ = netlink.LinkDel(link)
		return fmt.Errorf("netlink: set %s up: %w", name, err)
	}

	return nil
}

// AddPortMapping adds DNAT and FORWARD rules for a port mapping.
func AddPortMapping(hostInterface string, hostPort int, guestIP string, guestPort int, protocol string) error {
	if protocol == "" {
		protocol = "tcp"
	}

	// DNAT Rule
	dnatArgs := []string{"-t", "nat", "-A", "PREROUTING", "-p", protocol}
	if hostInterface != "" && hostInterface != "0.0.0.0" {
		if net.ParseIP(hostInterface) != nil {
			dnatArgs = append(dnatArgs, "-d", hostInterface)
		} else {
			dnatArgs = append(dnatArgs, "-i", hostInterface)
		}
	}
	dnatArgs = append(dnatArgs, "--dport", strconv.Itoa(hostPort), "-j", "DNAT", "--to-destination", fmt.Sprintf("%s:%d", guestIP, guestPort))

	if err := runCmd("iptables", dnatArgs...); err != nil {
		return fmt.Errorf("iptables dnat add: %w", err)
	}

	// DNAT Rule (Local Host Traffic)
	// We add this to the OUTPUT chain to allow the host to access its own published ports.
	if hostInterface == "" || hostInterface == "0.0.0.0" || hostInterface == "127.0.0.1" {
		outputArgs := []string{"-t", "nat", "-A", "OUTPUT", "-p", protocol, "--dport", strconv.Itoa(hostPort), "-j", "DNAT", "--to-destination", fmt.Sprintf("%s:%d", guestIP, guestPort)}
		_ = runCmd("iptables", outputArgs...)
	}

	// FORWARD Rule
	fwdArgs := []string{"-A", "FORWARD", "-p", protocol, "-d", guestIP, "--dport", strconv.Itoa(guestPort), "-j", "ACCEPT"}
	if err := runCmd("iptables", fwdArgs...); err != nil {
		// Rollback DNAT rules
		dnatArgs[3] = "-D"
		_ = runCmd("iptables", dnatArgs...)
		
		outputArgs := []string{"-t", "nat", "-D", "OUTPUT", "-p", protocol, "--dport", strconv.Itoa(hostPort), "-j", "DNAT", "--to-destination", fmt.Sprintf("%s:%d", guestIP, guestPort)}
		_ = runCmd("iptables", outputArgs...)

		return fmt.Errorf("iptables forward add: %w", err)
	}

	slog.Info("port mapping added", "host", hostInterface, "hPort", hostPort, "gIP", guestIP, "gPort", guestPort)
	return nil
}

// RemovePortMapping removes the DNAT and FORWARD rules for a port mapping.
func RemovePortMapping(hostInterface string, hostPort int, guestIP string, guestPort int, protocol string) error {
	if protocol == "" {
		protocol = "tcp"
	}

	dnatArgs := []string{"-t", "nat", "-D", "PREROUTING", "-p", protocol}
	if hostInterface != "" && hostInterface != "0.0.0.0" {
		if net.ParseIP(hostInterface) != nil {
			dnatArgs = append(dnatArgs, "-d", hostInterface)
		} else {
			dnatArgs = append(dnatArgs, "-i", hostInterface)
		}
	}
	dnatArgs = append(dnatArgs, "--dport", strconv.Itoa(hostPort), "-j", "DNAT", "--to-destination", fmt.Sprintf("%s:%d", guestIP, guestPort))
	_ = runCmd("iptables", dnatArgs...)

	// Remove OUTPUT chain rule if applicable
	if hostInterface == "" || hostInterface == "0.0.0.0" || hostInterface == "127.0.0.1" {
		outputArgs := []string{"-t", "nat", "-D", "OUTPUT", "-p", protocol, "--dport", strconv.Itoa(hostPort), "-j", "DNAT", "--to-destination", fmt.Sprintf("%s:%d", guestIP, guestPort)}
		_ = runCmd("iptables", outputArgs...)
	}

	fwdArgs := []string{"-D", "FORWARD", "-p", protocol, "-d", guestIP, "--dport", strconv.Itoa(guestPort), "-j", "ACCEPT"}
	_ = runCmd("iptables", fwdArgs...)

	slog.Info("port mapping removed", "host", hostInterface, "hPort", hostPort, "gIP", guestIP, "gPort", guestPort)
	return nil
}


// DeleteTap removes a TAP interface.
func DeleteTap(name string) error {
	slog.Info("deleting tap interface", "name", name)
	link, err := netlink.LinkByName(name)
	if err != nil {
		// Already gone — treat as success for idempotent cleanup.
		return nil
	}
	return netlink.LinkDel(link)
}

// getDefaultInterface reads the kernel routing table via netlink to find the outbound NIC.
func getDefaultInterface() (string, error) {
	routes, err := netlink.RouteList(nil, netlink.FAMILY_V4)
	if err != nil {
		return "", fmt.Errorf("netlink: list routes: %w", err)
	}
	for _, r := range routes {
		if r.Dst == nil || r.Dst.IP.Equal(net.IPv4zero) {
			if r.LinkIndex == 0 {
				continue
			}
			link, err := netlink.LinkByIndex(r.LinkIndex)
			if err != nil {
				return "", fmt.Errorf("netlink: link by index %d: %w", r.LinkIndex, err)
			}
			return link.Attrs().Name, nil
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
