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

	bootStart := time.Now()

	go reapZombies()

	// Phase 1: Mount /proc, /sys, /dev — fast (<5ms) and required before
	// AF_VSOCK works because the kernel needs these to finish probing the
	// virtio_vsock transport.
	if err := mountBaseFilesystems(); err != nil {
		die("Failed to mount base filesystems: %v", err)
	}

	// Phase 2: Start vsock listeners BEFORE the slow I/O (ext4 mount,
	// networking). Retry briefly in case virtio_vsock hasn't finished
	// initializing — the base mounts above give it time but there's a
	// small race window on fast boots.
	readyCh := make(chan struct{})

	go startExecServer(readyCh)

	log.Println("Starting vsock Control Plane on port 5000...")
	listener, err := listenVsock(5000)
	if err != nil {
		die("Failed to listen on vsock port 5000: %v", err)
	}
	defer listener.Close()
	log.Printf("vsock listener ready  %v", time.Since(bootStart))

	// Phase 3: Mount container rootfs and configure networking concurrently
	// while the host's vsock connect is already succeeding above.
	go func() {
		t0 := time.Now()
		if err := mountContainerFilesystems(); err != nil {
			die("Failed to mount container filesystems: %v", err)
		}
		log.Printf("container mounted    %v", time.Since(t0))

		t1 := time.Now()
		if err := configureNetworking(); err != nil {
			die("Networking bootstrap failed: %v", err)
		}
		log.Printf("networking ready     %v", time.Since(t1))
		log.Printf("boot total           %v", time.Since(bootStart))
		close(readyCh)
	}()

	for {
		log.Println("Waiting for host control connection...")
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("Failed to accept control connection: %v\n", err)
			continue
		}

		log.Println("Connection accepted. Entering Execution Bridge...")
		go func(c net.Conn) {
			defer c.Close()
			if err := handleExecution(c, readyCh); err != nil {
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

func mountBaseFilesystems() error {
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

func listenVsock(port uint32) (*vsock.Listener, error) {
	for i := 0; i < 50; i++ {
		l, err := vsock.Listen(port, nil)
		if err == nil {
			return l, nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	return vsock.Listen(port, nil)
}

func mountContainerFilesystems() error {
	for i := 0; i < 200; i++ {
		if _, err := os.Stat("/dev/vda"); err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}

	// Settle time after devtmpfs (was unconditional in the old single-path boot before
	// vsock was moved earlier). Avoids racing devpts / block mount on cold plug.
	time.Sleep(20 * time.Millisecond)

	_ = os.MkdirAll("/dev/pts", 0755)
	var devptsErr error
	for attempt := 0; attempt < 10; attempt++ {
		devptsErr = unix.Mount("devpts", "/dev/pts", "devpts", unix.MS_NOSUID|unix.MS_NOEXEC, "ptmxmode=0666")
		if devptsErr == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if devptsErr != nil {
		return fmt.Errorf("mount /dev/pts: %w", devptsErr)
	}

	if _, err := os.Stat("/dev/ptmx"); os.IsNotExist(err) {
		_ = os.Symlink("/dev/pts/ptmx", "/dev/ptmx")
	}

	containerRoot := "/mnt/container"
	_ = os.MkdirAll(containerRoot, 0755)
	var extErr error
	for attempt := 0; attempt < 20; attempt++ {
		extErr = unix.Mount("/dev/vda", containerRoot, "ext4", unix.MS_NOATIME, "")
		if extErr == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if extErr != nil {
		return fmt.Errorf("failed to mount container rootfs at /dev/vda: %w", extErr)
	}

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

	cmdline, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return fmt.Errorf("read cmdline: %w", err)
	}

	var ipStr, gwStr string
	parts := strings.Fields(string(cmdline))
	for _, p := range parts {
		if strings.HasPrefix(p, "ip=") {
			ipStr = strings.TrimPrefix(p, "ip=")
		} else if strings.HasPrefix(p, "gw=") {
			gwStr = strings.TrimPrefix(p, "gw=")
		}
	}

	if ipStr == "" || gwStr == "" {
		return fmt.Errorf("missing ip or gw in cmdline: %s", string(cmdline))
	}

	ip := net.ParseIP(ipStr)
	gw := net.ParseIP(gwStr)
	if ip == nil || gw == nil {
		return fmt.Errorf("invalid ip %q or gw %q", ipStr, gwStr)
	}

	addr := &netlink.Addr{
		IPNet: &net.IPNet{IP: ip, Mask: net.CIDRMask(32, 32)},
		Peer:  &net.IPNet{IP: gw, Mask: net.CIDRMask(32, 32)},
	}

	if err := netlink.AddrAdd(eth0, addr); err != nil {
		return fmt.Errorf("add address: %w", err)
	}
	if err := netlink.LinkSetUp(eth0); err != nil {
		return fmt.Errorf("up eth0: %w", err)
	}

	// 3. Default Gateway Route
	_, dest, _ := net.ParseCIDR("0.0.0.0/0")
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

func handleExecution(conn net.Conn, ready <-chan struct{}) error {
	decoder := json.NewDecoder(conn)
	var payload ExecPayload
	if err := decoder.Decode(&payload); err != nil {
		return fmt.Errorf("decode payload error: %w", err)
	}

	if len(payload.Command) == 0 {
		return fmt.Errorf("empty command received")
	}

	<-ready

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
	msg := fmt.Sprintf("FATAL: "+format+"\n", args...)
	log.Print(msg)
	if c, err := os.OpenFile("/dev/console", os.O_WRONLY, 0); err == nil {
		_, _ = fmt.Fprintf(c, "[herd-guest-agent] %s", msg)
		_ = c.Sync()
		_ = c.Close()
	}
	// Still attempt to cleanly end the MicroVM on failure instead of just exiting and staying idle
	_ = unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
	os.Exit(1)
}

func startExecServer(ready <-chan struct{}) {
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
			<-ready
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
