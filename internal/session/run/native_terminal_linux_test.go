//go:build linux

package run

// Verifies native foreground mode selection preserves the caller's terminal
// stream topology.

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/creack/pty"

	"petris.dev/toby/internal/sandbox/bwrap"
)

func TestNativeForegroundModePreservesRedirectedStreams(t *testing.T) {
	master, terminal, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := master.Close(); err != nil {
			t.Error(err)
		}
		if err := terminal.Close(); err != nil {
			t.Error(err)
		}
	})

	output, err := os.OpenFile(terminal.Name(), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := output.Close(); err != nil {
			t.Error(err)
		}
	})
	errorOutput, err := os.OpenFile(terminal.Name(), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := errorOutput.Close(); err != nil {
			t.Error(err)
		}
	})

	otherMaster, otherTerminal, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := otherMaster.Close(); err != nil {
			t.Error(err)
		}
		if err := otherTerminal.Close(); err != nil {
			t.Error(err)
		}
	})

	for _, test := range []struct {
		name    string
		managed bool
		stdin   io.Reader
		stdout  io.Writer
		stderr  io.Writer
		want    bwrap.ExecutionMode
	}{
		{
			name:    "managed-single-terminal",
			managed: true,
			stdin:   terminal,
			stdout:  output,
			stderr:  errorOutput,
			want:    bwrap.ExecutionManagedPTY,
		},
		{
			name:    "managed-redirected-stdout",
			managed: true,
			stdin:   terminal,
			stdout:  io.Discard,
			stderr:  errorOutput,
			want:    bwrap.ExecutionDirectTerminal,
		},
		{
			name:    "managed-redirected-stderr",
			managed: true,
			stdin:   terminal,
			stdout:  output,
			stderr:  io.Discard,
			want:    bwrap.ExecutionDirectTerminal,
		},
		{
			name:    "managed-distinct-output-terminal",
			managed: true,
			stdin:   terminal,
			stdout:  output,
			stderr:  otherTerminal,
			want:    bwrap.ExecutionDirectTerminal,
		},
		{
			name:    "managed-without-terminal-input",
			managed: true,
			stdin:   strings.NewReader("input"),
			stdout:  output,
			stderr:  errorOutput,
			want:    bwrap.ExecutionNonInteractive,
		},
		{
			name:    "unmanaged-terminal",
			managed: false,
			stdin:   terminal,
			stdout:  output,
			stderr:  errorOutput,
			want:    bwrap.ExecutionDirectTerminal,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := nativeForegroundMode(
				test.managed,
				test.stdin,
				test.stdout,
				test.stderr,
			)
			if got != test.want {
				t.Fatalf("mode = %q, want %q", got, test.want)
			}
		})
	}
}
