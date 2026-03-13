//go:build linux

package herd

import (
	"os"
	"testing"
)

// ---------------------------------------------------------------------------
// SeccompPolicy tests
// ---------------------------------------------------------------------------

func TestSeccompPolicyEnvValue(t *testing.T) {
	tests := []struct {
		policy SeccompPolicy
		want   string
	}{
		{SeccompPolicyOff, "off"},
		{SeccompPolicyLog, "log"},
		{SeccompPolicyErrno, "errno"},
		{SeccompPolicyKill, "kill"},
	}
	for _, tc := range tests {
		if got := tc.policy.envValue(); got != tc.want {
			t.Errorf("policy %d envValue: want %q, got %q", tc.policy, tc.want, got)
		}
	}
}

func TestParsePolicyEnv(t *testing.T) {
	tests := []struct {
		input string
		want  SeccompPolicy
	}{
		{"off", SeccompPolicyOff},
		{"log", SeccompPolicyLog},
		{"errno", SeccompPolicyErrno},
		{"kill", SeccompPolicyKill},
		{"", SeccompPolicyOff},
		{"unknown", SeccompPolicyOff},
	}
	for _, tc := range tests {
		if got := parsePolicyEnv(tc.input); got != tc.want {
			t.Errorf("parsePolicyEnv(%q): want %d, got %d", tc.input, tc.want, got)
		}
	}
}

// ---------------------------------------------------------------------------
// buildHTTPWorkerFilter tests
// ---------------------------------------------------------------------------

func TestBuildHTTPWorkerFilter_ReturnsNilForPolicyOff(t *testing.T) {
	// SeccompPolicyOff means EnterSandbox is a no-op; filter should not be built.
	// We test that EnterSandbox returns nil without loading anything.
	if err := os.Unsetenv("HERD_SECCOMP_PROFILE"); err != nil {
		t.Fatal(err)
	}
	if err := EnterSandbox(); err != nil {
		t.Errorf("EnterSandbox with unset env should be no-op, got: %v", err)
	}

	t.Setenv("HERD_SECCOMP_PROFILE", "off")
	if err := EnterSandbox(); err != nil {
		t.Errorf("EnterSandbox with PROFILE=off should be no-op, got: %v", err)
	}
}

func TestBuildHTTPWorkerFilter_ValidBPF(t *testing.T) {
	instructions, err := buildHTTPWorkerFilter(SeccompPolicyErrno)
	if err != nil {
		t.Fatalf("buildHTTPWorkerFilter: %v", err)
	}
	if len(instructions) == 0 {
		t.Error("expected non-empty BPF instructions for SeccompPolicyErrno")
	}
	// Sanity check: at least 10 instructions (arch check + syscall checks + return)
	const minExpectedInstructions = 10
	if len(instructions) < minExpectedInstructions {
		t.Errorf("expected at least %d BPF instructions, got %d", minExpectedInstructions, len(instructions))
	}
}

func TestBuildHTTPWorkerFilter_KillPolicy(t *testing.T) {
	instructions, err := buildHTTPWorkerFilter(SeccompPolicyKill)
	if err != nil {
		t.Fatalf("buildHTTPWorkerFilter(Kill): %v", err)
	}
	if len(instructions) == 0 {
		t.Error("expected non-empty BPF instructions for SeccompPolicyKill")
	}
}

// ---------------------------------------------------------------------------
// EnterSandbox no-op tests (safe to run without kernel seccomp support)
// ---------------------------------------------------------------------------

func TestEnterSandbox_NoopWhenEnvUnset(t *testing.T) {
	t.Setenv("HERD_SECCOMP_PROFILE", "")
	if err := EnterSandbox(); err != nil {
		t.Errorf("expected no-op when env unset, got: %v", err)
	}
}

func TestEnterSandbox_NoopWhenOff(t *testing.T) {
	t.Setenv("HERD_SECCOMP_PROFILE", "off")
	if err := EnterSandbox(); err != nil {
		t.Errorf("expected no-op when PROFILE=off, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// ProcessFactory seccomp defaults
// ---------------------------------------------------------------------------

func TestProcessFactory_DefaultSeccompPolicyIsErrno(t *testing.T) {
	f := NewProcessFactory("echo")
	if f.seccompPolicy != SeccompPolicyErrno {
		t.Errorf("expected default seccompPolicy == SeccompPolicyErrno (%d), got %d",
			SeccompPolicyErrno, f.seccompPolicy)
	}
}

func TestProcessFactory_WithSeccompPolicy(t *testing.T) {
	f := NewProcessFactory("echo").WithSeccompPolicy(SeccompPolicyOff)
	if f.seccompPolicy != SeccompPolicyOff {
		t.Errorf("expected SeccompPolicyOff after WithSeccompPolicy, got %d", f.seccompPolicy)
	}
}

func TestProcessFactory_WithSeccompPolicy_Kill(t *testing.T) {
	f := NewProcessFactory("echo").WithSeccompPolicy(SeccompPolicyKill)
	if f.seccompPolicy != SeccompPolicyKill {
		t.Errorf("expected SeccompPolicyKill after WithSeccompPolicy, got %d", f.seccompPolicy)
	}
}
