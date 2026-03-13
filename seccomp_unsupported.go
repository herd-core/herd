//go:build !linux

package herd

// SeccompPolicy controls how unauthorized syscalls are handled.
// On non-Linux systems, all policies are treated as SeccompPolicyOff.
type SeccompPolicy int

const (
	// SeccompPolicyOff disables seccomp filtering (only valid value on non-Linux).
	SeccompPolicyOff SeccompPolicy = iota
	// SeccompPolicyLog — no-op on non-Linux.
	SeccompPolicyLog
	// SeccompPolicyErrno — no-op on non-Linux.
	SeccompPolicyErrno
	// SeccompPolicyKill — no-op on non-Linux.
	SeccompPolicyKill
)

func (p SeccompPolicy) envValue() string { return "off" }

// EnterSandbox is a no-op on non-Linux systems.
// It exists so worker binaries can call it unconditionally without build tags.
func EnterSandbox() error { return nil }
