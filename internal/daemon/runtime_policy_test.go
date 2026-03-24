package daemon

import (
	"io"
	"log"
	"strings"
	"testing"
)

func TestEnforceRuntimePolicy_Linux(t *testing.T) {
	t.Parallel()

	if err := EnforceRuntimePolicy("linux", log.New(io.Discard, "", 0)); err != nil {
		t.Fatalf("expected linux to pass, got error: %v", err)
	}
}

func TestEnforceRuntimePolicy_Darwin(t *testing.T) {
	t.Parallel()

	if err := EnforceRuntimePolicy("darwin", log.New(io.Discard, "", 0)); err != nil {
		t.Fatalf("expected darwin to pass with warnings, got error: %v", err)
	}
}

func TestEnforceRuntimePolicy_Unsupported(t *testing.T) {
	t.Parallel()

	err := EnforceRuntimePolicy("windows", log.New(io.Discard, "", 0))
	if err == nil {
		t.Fatal("expected unsupported platform error")
	}
	if !strings.Contains(err.Error(), "unsupported platform") {
		t.Fatalf("unexpected error: %v", err)
	}
}
