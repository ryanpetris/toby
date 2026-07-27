//go:build linux

package sandbox

// Verifies the standalone sandbox-helper informational command surface.

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestExecuteHelpListsSandboxCommands(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Execute(
		[]string{"tobys", "--help"},
		io.NopCloser(strings.NewReader("")),
		&stdout,
		&stderr,
	)

	if code != 0 {
		t.Fatalf("help exit code = %d, want 0", code)
	}
	for _, command := range []string{
		"tobys resource connect",
		"tobys exec",
	} {
		if !strings.Contains(stdout.String(), command) {
			t.Fatalf("help output %q does not contain %q", stdout.String(), command)
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("help stderr = %q, want empty", stderr.String())
	}
}

func TestExecuteRejectsUnsupportedCommand(t *testing.T) {
	var stderr bytes.Buffer

	code := Execute(
		[]string{"tobys", "other"},
		io.NopCloser(strings.NewReader("")),
		io.Discard,
		&stderr,
	)

	if code != 2 {
		t.Fatalf("unsupported command exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unsupported tobys command") {
		t.Fatalf("unsupported command stderr = %q", stderr.String())
	}
}
