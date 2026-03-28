package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/mdlayher/vsock"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// ExecPayload matches the struct from the host's internal/vsock package
type ExecPayload struct {
	Command []string `json:"command"`
}

func main() {
	// Wrap stdout and stderr to guarantee prefixing for all outputs including panics
	origStdout := os.Stdout
	origStderr := os.Stderr

	rOut, wOut, _ := os.Pipe()
	os.Stdout = wOut

	rErr, wErr, _ := os.Pipe()
	os.Stderr = wErr
	log.SetOutput(wErr) // ensure log uses the captured stderr

	go func() {
		scanner := bufio.NewScanner(rOut)
		for scanner.Scan() {
			fmt.Fprintln(origStdout, "[herd-guest-agent]", scanner.Text())
		}
	}()
	go func() {
		scanner := bufio.NewScanner(rErr)
		for scanner.Scan() {
			fmt.Fprintln(origStderr, "[herd-guest-agent]", scanner.Text())
		}
	}()

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

	// 4. The Exec Server (Vsock Port 5001)
	go startExecServer()

	// 5. The Control Plane (Vsock Server)
	log.Println("Starting vsock Control Plane on port 5000...")
	listener, err := vsock.Listen(5000, nil)
	if err != nil {
		die("Failed to listen on vsock port 5000: %v", err)
	}
	defer listener.Close()

	for {
		log.Println("Waiting for host control connection...")
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("Failed to accept control connection: %v\n", err)
			continue
		}

		// 5. The Execution Bridge
		log.Println("Connection accepted. Entering Execution Bridge...")
		go func(c net.Conn) {
			defer c.Close()
			if err := handleExecution(c); err != nil {
				log.Printf("Execution bridge error: %v\n", err)
			}
		}(conn)
	}
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
	// Base initrd mounts so PID 1 can actually exist
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

	// Give the kernel time to populate devtmpfs
	time.Sleep(20 * time.Millisecond)

	// Mount devpts for PTY support (crucial for 'herd exec')
	_ = os.MkdirAll("/dev/pts", 0755)
	if err := unix.Mount("devpts", "/dev/pts", "devpts", unix.MS_NOSUID|unix.MS_NOEXEC, "ptmxmode=0666"); err != nil {
		return fmt.Errorf("mount /dev/pts: %w", err)
	}

	// Ensure /dev/ptmx exists (devtmpfs might provide a node, but linking /dev/pts/ptmx is safer)
	if _, err := os.Stat("/dev/ptmx"); os.IsNotExist(err) {
		_ = os.Symlink("/dev/pts/ptmx", "/dev/ptmx")
	}

	// Magic: Mount the Firecracker drive to a separate directory
	containerRoot := "/mnt/container"
	_ = os.MkdirAll(containerRoot, 0755)
	if err := unix.Mount("/dev/vda", containerRoot, "ext4", 0, ""); err != nil {
		return fmt.Errorf("failed to mount container rootfs at /dev/vda: %w", err)
	}

	// Bind mount virtual filesystems recursively into the container directory
	for _, m := range []string{"/proc", "/sys", "/dev"} {
		dest := containerRoot + m
		_ = os.MkdirAll(dest, 0755)
		if err := unix.Mount(m, dest, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
			return fmt.Errorf("bind mount %s: %w", m, err)
		}
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
		log.Printf("find eth0: %v (skipping network setup)\n", err)
		return nil
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
	dnsConfig := []byte("nameserver 8.8.8.8\nnameserver 1.1.1.1\n")
	_ = os.MkdirAll("/etc", 0755)
	if err := os.WriteFile("/etc/resolv.conf", dnsConfig, 0644); err != nil {
		return fmt.Errorf("write resolv.conf: %w", err)
	}

	// Inject into the container rootfs where the user workload runs
	_ = os.MkdirAll("/mnt/container/etc", 0755)
	_ = os.Remove("/mnt/container/etc/resolv.conf") // remove symlinks to outside
	if err := os.WriteFile("/mnt/container/etc/resolv.conf", dnsConfig, 0644); err != nil {
		return fmt.Errorf("write container resolv.conf: %w", err)
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

	bin := payload.Command[0]
	// Resolve relative binaries inside the container's paths
	if !strings.HasPrefix(bin, "/") {
		paths := []string{"/usr/local/bin", "/usr/bin", "/bin", "/usr/local/sbin", "/usr/sbin", "/sbin"}
		for _, p := range paths {
			if _, err := os.Stat("/mnt/container" + p + "/" + bin); err == nil {
				bin = p + "/" + bin
				break
			}
		}
	}

	if !strings.HasPrefix(bin, "/") {
		return fmt.Errorf("executable %q not found in container's standard paths", payload.Command[0])
	}

	// Prepare execution bypassing exec.Command to directly inject the resolved absolute path
	// This prevents exec.LookPath from trying (and failing) to find it in the bare initrd
	cmd := &exec.Cmd{
		Path: bin,
		Args: payload.Command,
	}

	// Wire I/O directly to the connection
	cmd.Stdout = conn
	cmd.Stderr = conn
	cmd.Stdin = conn

	// Lock the execution strictly inside the mounted container filesystem!
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Chroot: "/mnt/container",
	}

	// Inject sensible default PATH for the workload
	cmd.Env = []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	}

	// Run synchronously
	return cmd.Run()
}

func die(format string, args ...any) {
	log.Printf("FATAL: "+format+"\n", args...)
	// Still attempt to cleanly end the MicroVM on failure instead of just exiting and staying idle
	_ = unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
	os.Exit(1)
}

func startExecServer() {
	log.Println("Starting vsock Exec Server on port 5001...")
	listener, err := vsock.Listen(5001, nil)
	if err != nil {
		log.Printf("Failed to listen on vsock port 5001: %v\n", err)
		return
	}
	defer listener.Close()

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("Failed to accept exec connection: %v\n", err)
			continue
		}

		log.Println("Exec connection accepted. Spawning interactive shell...")
		go func(c net.Conn) {
			defer c.Close()
			if err := handleInteractiveShell(c); err != nil {
				log.Printf("Interactive shell error: %v\n", err)
			}
		}(conn)
	}
}

func handleInteractiveShell(conn net.Conn) error {
	bin := "/bin/bash"
	// Check if sh exists in the standard location inside the chroot
	if _, err := os.Stat("/mnt/container" + bin); err != nil {
		// Fallback or let exec.Cmd fail
		log.Printf("Warning: %s not found in %s: %v", bin, "/mnt/container"+bin, err)
	}

	cmd := &exec.Cmd{
		Path: bin,
	}

	// Lock the execution strictly inside the mounted container filesystem!
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Chroot: "/mnt/container",
	}

	// Inject sensible default PATH for the workload
	cmd.Env = []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"TERM=xterm",
	}

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return fmt.Errorf("pty start error: %w", err)
	}
	defer ptmx.Close()

	// Wait for shell to exit, but also copy I/O asynchronously
	go func() {
		_, _ = io.Copy(ptmx, conn)
	}()

	go func() {
		_, _ = io.Copy(conn, ptmx)
	}()

	return cmd.Wait()
}
