package core

// SandboxConfig contains per-worker sandbox resource constraints.
// A value of 0 means "unlimited" for memory and CPU.
type SandboxConfig struct {
	MemoryMaxBytes int64
	CpuMaxMicros   int64
	PidsMax        int64
	CloneFlags     uintptr
}

// SandboxHandle owns post-start and cleanup hooks for sandbox resources.
// Implementations may be no-op on unsupported or soft-fail paths.
type SandboxHandle interface {
	PostStart()
	Cleanup()
}
