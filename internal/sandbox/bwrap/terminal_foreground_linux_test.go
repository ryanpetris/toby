//go:build linux

package bwrap

// Exercises managed-terminal approvals, suspension, resizing, and I/O failures
// without a Bubblewrap child.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	sandboxapi "petris.dev/toby/internal/sandbox"
)

func TestTerminalForegroundApprovalDefaultsToDenyAndCanApprove(t *testing.T) {
	inputReader, inputWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inputWriter.Close()
	outputReader, outputWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer outputReader.Close()
	defer outputWriter.Close()

	var toolInput bytes.Buffer
	var registered sandboxapi.ApprovalPrompter
	foreground, err := newTerminalForeground(
		inputReader,
		outputWriter,
		&toolInput,
		func(prompter sandboxapi.ApprovalPrompter) {
			registered = prompter
		},
		nil,
		nil,
		nil,
		80,
		24,
	)
	if err != nil {
		t.Fatal(err)
	}
	if registered != foreground {
		t.Fatal("foreground did not register its approval prompter")
	}

	assertTerminalDecision(t, foreground, inputWriter, "\r", false)
	assertTerminalDecision(t, foreground, inputWriter, "a", true)

	if err := foreground.Close(); err != nil {
		t.Fatal(err)
	}
	if registered != nil {
		t.Fatal("foreground did not unregister its approval prompter")
	}
	if toolInput.Len() != 0 {
		t.Fatalf("approval input reached the tool: %q", toolInput.String())
	}
}

func TestTerminalForegroundUsesConfiguredSuspendCharacter(t *testing.T) {
	tests := []struct {
		name        string
		enabled     bool
		wantInput   string
		wantSuspend int
	}{
		{
			name:        "custom",
			enabled:     true,
			wantInput:   "beforeafter",
			wantSuspend: 1,
		},
		{
			name:        "disabled",
			enabled:     false,
			wantInput:   "before\x19after",
			wantSuspend: 0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inputReader, inputWriter, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer inputReader.Close()
			defer inputWriter.Close()
			outputReader, outputWriter, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer outputReader.Close()
			defer outputWriter.Close()

			var toolInput bytes.Buffer
			suspensions := 0
			foreground, err := newTerminalForeground(
				inputReader,
				outputWriter,
				&toolInput,
				nil,
				func() (int, int, bool, error) {
					suspensions++
					return 91, 37, true, nil
				},
				nil,
				func() (byte, bool) {
					return 0x19, test.enabled
				},
				80,
				24,
			)
			if err != nil {
				t.Fatal(err)
			}

			if err := foreground.onInput(
				[]byte("before\x19after"),
			); err != nil {
				t.Fatal(err)
			}
			if got := toolInput.String(); got != test.wantInput {
				t.Fatalf("tool input = %q, want %q", got, test.wantInput)
			}
			if suspensions != test.wantSuspend {
				t.Fatalf(
					"suspensions = %d, want %d",
					suspensions,
					test.wantSuspend,
				)
			}
			if test.enabled &&
				(foreground.width != 91 || foreground.height != 37) {
				t.Fatalf(
					"resumed size = %dx%d, want 91x37",
					foreground.width,
					foreground.height,
				)
			}

			if err := foreground.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTerminalForegroundPropagatesInteractiveWrites(t *testing.T) {
	inputReader, inputWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inputReader.Close()
	defer inputWriter.Close()
	outputReader, outputWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer outputReader.Close()
	defer outputWriter.Close()

	sentinel := errors.New("terminal write failed")
	foreground, err := newTerminalForeground(
		inputReader,
		outputWriter,
		errorWriter{err: sentinel},
		nil,
		nil,
		nil,
		nil,
		80,
		24,
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := foreground.onInput([]byte("input")); !errors.Is(err, sentinel) {
		t.Fatalf("tool input error = %v, want %v", err, sentinel)
	}
	for _, outputErr := range []error{sentinel, syscall.EIO, os.ErrClosed} {
		foreground.output = errorWriter{err: outputErr}
		if err := foreground.PumpOutput(
			strings.NewReader("output"),
		); !errors.Is(err, outputErr) {
			t.Fatalf("host output error = %v, want %v", err, outputErr)
		}
	}

	if err := foreground.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSanitizeTerminalModalTextPreservesPrintableText(t *testing.T) {
	const text = "Deploy café to 東京?"

	if got := sanitizeTerminalModalText(text, 128); got != text {
		t.Fatalf("sanitized text = %q, want %q", got, text)
	}
}

func TestSanitizeTerminalModalTextReplacesControlsAndBoundsOutput(t *testing.T) {
	const malicious = "Run\x1b[2J\nApprove\t\u009b31m\u202eforged"
	const want = "Run [2J Approve  31m forged"

	if got := sanitizeTerminalModalText(malicious, 128); got != want {
		t.Fatalf("sanitized text = %q, want %q", got, want)
	}
	if got := sanitizeTerminalModalText("abcdef", 4); got != "abcd" {
		t.Fatalf("bounded text = %q, want %q", got, "abcd")
	}
}

func TestRenderTerminalModalDoesNotReplayRequestControls(t *testing.T) {
	modal := renderTerminalModal(
		sandboxapi.ApprovalRequest{
			Name:    "Safe\x1b[2J\nFORGED-TITLE",
			Message: "Details\r\nFORGED-CHOICE\x1b[?1049l",
		},
		terminalDenySelection,
	)

	for _, unsafe := range []string{
		"\x1b[2J",
		"\x1b[?1049l",
		"\nFORGED-TITLE",
		"\r\nFORGED-CHOICE",
	} {
		if strings.Contains(modal, unsafe) {
			t.Fatalf("modal replays unsafe request content %q", unsafe)
		}
	}
	for _, safe := range []string{
		"Safe [2J FORGED-TITLE",
		"Details  FORGED-CHOICE [?1049l",
	} {
		if !strings.Contains(modal, safe) {
			t.Fatalf("modal does not contain sanitized text %q", safe)
		}
	}
}

type errorWriter struct {
	err error
}

func (w errorWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func assertTerminalDecision(
	t *testing.T,
	foreground *terminalForeground,
	input *os.File,
	key string,
	want bool,
) {
	t.Helper()

	result := make(chan struct {
		allow bool
		err   error
	}, 1)
	go func() {
		allow, err := foreground.PromptApproval(
			context.Background(),
			sandboxapi.ApprovalRequest{
				Action:  "test.action",
				Name:    "Test action",
				Message: "exercise the modal",
			},
		)
		result <- struct {
			allow bool
			err   error
		}{allow: allow, err: err}
	}()

	deadline := time.Now().Add(time.Second)
	for {
		foreground.mu.Lock()
		visible := foreground.modal
		foreground.mu.Unlock()
		if visible {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("approval modal did not become visible")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := input.Write([]byte(key)); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-result:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.allow != want {
			t.Fatalf("approval = %t, want %t", got.allow, want)
		}
	case <-time.After(time.Second):
		t.Fatal("approval decision timed out")
	}
}
