//go:build linux

package run

// Verifies lifecycle presentation labels never expose command executables.

import (
	"testing"

	"petris.dev/toby/internal/sandbox"
)

func TestLifecycleOperationLabelUsesIntentOrGenericFallback(t *testing.T) {
	if got := lifecycleOperationLabel(sandbox.ExecOptions{
		Status: "  Preparing storage  ",
	}); got != "Preparing storage" {
		t.Fatalf("explicit label = %q", got)
	}
	if got := lifecycleOperationLabel(sandbox.ExecOptions{}); got != "Working" {
		t.Fatalf("fallback label = %q", got)
	}
}
