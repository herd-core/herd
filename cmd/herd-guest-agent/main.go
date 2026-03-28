package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/mdlayher/vsock"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// ExecPayload matches the struct from the host's internal/vsock package
type ExecPayload struct {
	Command []string `json:"command"`
}

func main() {
	// 1. The PID 1 Reality Check: Zombie Reaping
	go reapZombies()

	// 2. Mounting the World
	if err := mountVirtualFilesystems(); err != nil {
		die("Failed to mount filesystems: %v", err)
	}

	// 3. Bootstrapping the Data Plane (Networking)
	if err := configureNetworking(); err != nil {
		die("Networking bootstrap failed: %v", err)
	}

	// 4. The Control Plane (Vsock Server)
	log.Println("Starting vsock Control Plane on port 5000...")
	listener, err := vsock.Listen(5000, nil)
	if err != nil {
		die("Failed to listen on vsock port 5000: %v", err)
	}
	defer listener.Close()

	log.Println("Waiting for host control connection...")
	conn, err := listener.Accept()
	if err != nil {
		die("Failed to accept control connection: %v", err)
	}
	defer conn.Close()

	// 5. The Execution Bridge
	log.Println("Connection accepted. Entering Execution Bridge...")
	if err := handleExecution(conn); err != nil {
		log.Printf("Execution bridge error: %v\n", err)
	}

	// Graceful shutdown and microVM termination
	log.Println("Workload completed. Terminating microVM...")
	listener.Close()
	conn.Close()
	_ = unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
}

func reapZombies() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGCHLD)

	for range c {
		var status unix.WaitStatus
		// Loop intensely through Wait4 to catch all dead children
		for {
			pid, err := unix.Wait4(-1, &status, unix.WNOHANG, nil)
			if err != nil || pid <= 0 {
				break
			}
		}
	}
}

func mountVirtualFilesystems() error {
	// Ignore errors if the directory doesn't exist yet, we attempt to create it.
	_ = os.MkdirAll("/proc", 0755)
	if err := unix.Mount("proc", "/proc", "proc", unix.MS_NODEV|unix.MS_NOEXEC|unix.MS_NOSUID, ""); err != nil {
		return fmt.Errorf("mount /proc: %w", err)
	}

	_ = os.MkdirAll("/sys", 0755)
	if err := unix.Mount("sysfs", "/sys", "sysfs", unix.MS_NODEV|unix.MS_NOEXEC|unix.MS_NOSUID, ""); err != nil {
		return fmt.Errorf("mount /sys: %w", err)
	}

	_ = os.MkdirAll("/dev", 0755)
	if err := unix.Mount("devtmpfs", "/dev", "devtmpfs", unix.MS_NOSUID, ""); err != nil {
		return fmt.Errorf("mount /dev: %w", err)
	}

	return nil
}

func configureNetworking() error {
	// Wait a moment for TAP interface to visibly attach to the VM
	// in some hypervisor configurations.
	
	// 1. Loopback
	lo, err := netlink.LinkByName("lo")
	if err != nil {
		return fmt.Errorf("find lo: %w", err)
	}
	if err := netlink.LinkSetUp(lo); err != nil {
		return fmt.Errorf("up lo: %w", err)
	}

	// 2. Primary Interface
	eth0, err := netlink.LinkByName("eth0")
	if err != nil {
		return fmt.Errorf("find eth0: %w", err)
	}

	addr, err := netlink.ParseAddr("172.16.0.2/24")
	if err != nil {
		return fmt.Errorf("parse address: %w", err)
	}
	if err := netlink.AddrAdd(eth0, addr); err != nil {
		return fmt.Errorf("add address: %w", err)
	}
	if err := netlink.LinkSetUp(eth0); err != nil {
		return fmt.Errorf("up eth0: %w", err)
	}

	// 3. Default Gateway Route
	_, dest, _ := net.ParseCIDR("0.0.0.0/0")
	gw := net.ParseIP("172.16.0.1")
	route := &netlink.Route{
		LinkIndex: eth0.Attrs().Index,
		Dst:       dest,
		Gw:        gw,
	}
	if err := netlink.RouteAdd(route); err != nil {
		return fmt.Errorf("route add: %w", err)
	}

	// 4. DNS
	// Make sure /etc exists
	_ = os.MkdirAll("/etc", 0755)
	if err := os.WriteFile("/etc/resolv.conf", []byte("nameserver 1.1.1.1\n"), 0644); err != nil {
		return fmt.Errorf("write resolv.conf: %w", err)
	}

	return nil
}

func handleExecution(conn net.Conn) error {
	// Parse the JSON payload arriving over vsock
	decoder := json.NewDecoder(conn)
	var payload ExecPayload
	if err := decoder.Decode(&payload); err != nil {
		return fmt.Errorf("decode payload error: %w", err)
	}

	if len(payload.Command) == 0 {
		return fmt.Errorf("empty command received")
	}

	// Prepare execution
	cmd := exec.Command(payload.Command[0], payload.Command[1:]...)

	// Wire I/O directly to the connection
	cmd.Stdout = conn
	cmd.Stderr = conn
	cmd.Stdin = conn

	// Run synchronously
	return cmd.Run()
}

func die(format string, args ...any) {
	log.Printf("FATAL: "+format+"\n", args...)
	// Still attempt to cleanly end the MicroVM on failure instead of just exiting and staying idle
	_ = unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
	os.Exit(1)
}
