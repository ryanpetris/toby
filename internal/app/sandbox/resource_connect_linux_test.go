//go:build linux

package sandbox

// Verifies exact early resource dispatch, raw stream routing, diagnostics,
// fallthrough, and signal exit behavior.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"

	"petris.dev/toby/internal/sandbox/layout"
)

func TestDispatchResourceConnectorDoesNotDependOnProcessConfiguration(
	t *testing.T,
) {
	t.Setenv("TOBY_SANDBOX", "")
	t.Setenv("XDG_CONFIG_HOME", "relative")

	payload := []byte{0x00, 0xff, '{', '}', '\n'}
	stdin := io.NopCloser(bytes.NewReader(payload))
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	var gotPath string
	var gotTarget string

	code, handled := dispatchResourceConnector(
		[]string{
			"/toby/bin/tobys",
			"resource",
			"connect",
			"--",
			"-docs.api",
		},
		stdin,
		&stdout,
		&stderr,
		func(
			_ context.Context,
			path string,
			target string,
			input io.ReadCloser,
			output io.Writer,
		) error {
			gotPath = path
			gotTarget = target

			data, err := io.ReadAll(input)
			if err != nil {
				return err
			}
			_, err = output.Write(data)
			return err
		},
		quietResourceConnectorSignals,
	)
	if !handled {
		t.Fatal("exact sandbox resource invocation was not handled")
	}
	if code != 0 {
		t.Fatalf("dispatch code = %d, want 0", code)
	}
	if gotPath != layout.SandboxSocket() {
		t.Fatalf(
			"connector path = %q, want %q",
			gotPath,
			layout.SandboxSocket(),
		)
	}
	if gotTarget != "-docs.api" {
		t.Fatalf(
			"connector target = %q, want %q",
			gotTarget,
			"-docs.api",
		)
	}
	if !bytes.Equal(stdout.Bytes(), payload) {
		t.Fatalf("stdout = %v, want %v", stdout.Bytes(), payload)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestDispatchResourceConnectorFallsThroughUnrelatedAndInvalidShapes(
	t *testing.T,
) {
	tests := []struct {
		name      string
		arguments []string
	}{
		{
			name:      "root help",
			arguments: []string{"tobys", "--help"},
		},
		{
			name:      "resource help",
			arguments: []string{"tobys", "resource", "--help"},
		},
		{
			name:      "connect help",
			arguments: []string{"tobys", "resource", "connect", "--help"},
		},
		{
			name:      "connect short help",
			arguments: []string{"tobys", "resource", "connect", "-h"},
		},
		{
			name:      "missing target",
			arguments: []string{"tobys", "resource", "connect", "--"},
		},
		{
			name: "extra target",
			arguments: []string{
				"tobys",
				"resource",
				"connect",
				"--",
				"one",
				"two",
			},
		},
		{
			name:      "manual command without generated separator",
			arguments: []string{"tobys", "resource", "connect", "target"},
		},
		{
			name: "root flag",
			arguments: []string{
				"tobys",
				"--debug",
				"resource",
				"connect",
				"target",
			},
		},
		{
			name:      "unrelated command",
			arguments: []string{"tobys", "exec", "environment"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			code, handled := dispatchResourceConnector(
				test.arguments,
				io.NopCloser(strings.NewReader("")),
				io.Discard,
				io.Discard,
				func(
					context.Context,
					string,
					string,
					io.ReadCloser,
					io.Writer,
				) error {
					t.Fatal("connector ran for a fallthrough invocation")
					return nil
				},
				func() (<-chan os.Signal, func()) {
					t.Fatal("signals registered for a fallthrough invocation")
					return nil, func() {}
				},
			)
			if handled {
				t.Fatalf(
					"dispatch = (%d, true), want (0, false)",
					code,
				)
			}
			if code != 0 {
				t.Fatalf("fallthrough code = %d, want 0", code)
			}
		})
	}
}

func TestDispatchResourceConnectorKeepsDiagnosticsOffStdout(t *testing.T) {
	sentinel := errors.New("connector diagnostic")
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code, handled := dispatchResourceConnector(
		[]string{"tobys", "resource", "connect", "--", "target"},
		io.NopCloser(strings.NewReader("")),
		&stdout,
		&stderr,
		func(
			context.Context,
			string,
			string,
			io.ReadCloser,
			io.Writer,
		) error {
			return sentinel
		},
		quietResourceConnectorSignals,
	)
	if !handled {
		t.Fatal("exact sandbox resource invocation was not handled")
	}
	if code != 1 {
		t.Fatalf("dispatch code = %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if got := stderr.String(); got != sentinel.Error()+"\n" {
		t.Fatalf("stderr = %q, want %q", got, sentinel.Error()+"\n")
	}
}

func TestDispatchResourceConnectorMapsSignalExitAndCancels(t *testing.T) {
	signals := make(chan os.Signal, 1)
	signals <- syscall.SIGTERM
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code, handled := dispatchResourceConnector(
		[]string{"tobys", "resource", "connect", "--", "target"},
		io.NopCloser(strings.NewReader("")),
		&stdout,
		&stderr,
		func(
			ctx context.Context,
			_ string,
			_ string,
			_ io.ReadCloser,
			_ io.Writer,
		) error {
			<-ctx.Done()
			return ctx.Err()
		},
		func() (<-chan os.Signal, func()) {
			return signals, func() {}
		},
	)
	if !handled {
		t.Fatal("exact sandbox resource invocation was not handled")
	}
	if code != 128+int(syscall.SIGTERM) {
		t.Fatalf(
			"dispatch code = %d, want %d",
			code,
			128+int(syscall.SIGTERM),
		)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func quietResourceConnectorSignals() (<-chan os.Signal, func()) {
	return nil, func() {}
}
