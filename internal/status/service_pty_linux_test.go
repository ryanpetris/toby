//go:build linux

package status

// Exercises live startup rendering through an actual pseudoterminal.

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"

	"petris.dev/toby/internal/diagnostic"
)

func TestServiceRendersLiveStatusThroughTerminalStderr(t *testing.T) {
	service, master := newPTYStatusService(t)
	if err := service.Begin(Options{}); err != nil {
		t.Fatal(err)
	}

	operation := service.StartOperation("Preparing live status")
	output := readPTYUntil(t, master, "Preparing live status")
	screen := renderedPTYScreen(t, output)
	if !strings.Contains(screen, "Preparing live status") {
		t.Fatalf("live terminal screen = %q", screen)
	}

	operation.Finish(nil)
}

func TestServiceRendersLiveOCIProgressThroughTerminalStderr(t *testing.T) {
	service, master := newPTYStatusService(t)
	if err := service.Begin(Options{}); err != nil {
		t.Fatal(err)
	}

	operation := service.StartOperation("Pulling OCI image")
	operation.SetProgress(Progress{
		CompletedBytes: 50,
		TotalBytes:     100,
		OCIAction:      "Pulling",
		OCIReference:   "docker.io/library/alpine:latest",
	})

	output := readPTYUntil(t, master, "alpine:latest")
	screen := renderedPTYScreen(t, output)
	for _, expected := range []string{
		"Preparing OCI image",
		"alpine:latest",
		"Pulling",
		"50%",
	} {
		if !strings.Contains(screen, expected) {
			t.Fatalf(
				"live OCI terminal screen does not contain %q: %q",
				expected,
				screen,
			)
		}
	}

	operation.Finish(nil)
}

func newPTYStatusService(t *testing.T) (*Service, *os.File) {
	t.Helper()

	master, terminal, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := terminal.Close(); err != nil {
			t.Error(err)
		}
		if err := master.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := pty.Setsize(master, &pty.Winsize{
		Rows: 24,
		Cols: 120,
	}); err != nil {
		t.Fatal(err)
	}

	diagnostics, err := diagnostic.NewService(diagnostic.Options{
		Level:  slog.LevelDebug,
		Format: diagnostic.FormatText,
		Stderr: terminal,
	})
	if err != nil {
		t.Fatal(err)
	}

	service := newServiceWithStderr(diagnostics, terminal)
	t.Cleanup(func() {
		if err := service.Finish(nil); err != nil {
			t.Error(err)
		}
	})

	return service, master
}

func readPTYUntil(t *testing.T, master *os.File, expected string) []byte {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	if err := master.SetReadDeadline(deadline); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	buffer := make([]byte, 4096)
	for !bytes.Contains(output.Bytes(), []byte(expected)) {
		count, err := master.Read(buffer)
		if count > 0 {
			written, writeErr := output.Write(buffer[:count])
			if writeErr != nil || written != count {
				t.Fatalf(
					"buffer terminal output: wrote %d of %d bytes: %v",
					written,
					count,
					writeErr,
				)
			}
		}
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				t.Fatalf(
					"timed out waiting for %q in terminal output: %q",
					expected,
					output.Bytes(),
				)
			}
			if errors.Is(err, io.EOF) {
				t.Fatalf(
					"terminal closed waiting for %q: %q",
					expected,
					output.Bytes(),
				)
			}
			t.Fatalf("read terminal output: %v", err)
		}
	}

	return output.Bytes()
}

func renderedPTYScreen(t *testing.T, output []byte) string {
	t.Helper()

	terminal := vt.NewEmulator(120, 24)
	screenOutput := strings.NewReplacer(
		ansi.RequestModeSynchronizedOutput, "",
		ansi.RequestModeUnicodeCore, "",
	).Replace(string(output))
	if _, err := terminal.Write([]byte(screenOutput)); err != nil {
		t.Fatal(err)
	}

	return terminal.String()
}
