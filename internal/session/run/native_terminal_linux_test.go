//go:build linux

package run

// Verifies native foreground mode selection.

import (
	"io"
	"strings"
	"testing"

	"github.com/creack/pty"

	"petris.dev/toby/internal/sandbox/bwrap"
)

func TestNativeForegroundModeSelectsByTerminalInput(t *testing.T) {
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

	for _, test := range []struct {
		name  string
		stdin io.Reader
		want  bwrap.ExecutionMode
	}{
		{
			name:  "terminal-input",
			stdin: terminal,
			want:  bwrap.ExecutionDirectTerminal,
		},
		{
			name:  "redirected-input",
			stdin: strings.NewReader("input"),
			want:  bwrap.ExecutionNonInteractive,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := nativeForegroundMode(test.stdin)
			if got != test.want {
				t.Fatalf("mode = %q, want %q", got, test.want)
			}
		})
	}
}
