//go:build linux

package bwrap

// Verifies direct process execution, status and stream preservation, descriptor
// ownership, managed PTY I/O, and cancellation of an entire process group.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestExecutorPreservesPlainStreamsInputAndExitStatus(t *testing.T) {
	executor := testExecutor(t, 100*time.Millisecond)
	invocation := &Invocation{
		Mode: ExecutionNonInteractive,
		Args: []string{
			"-c",
			`IFS= read -r value; printf "out:%s" "$value"; printf "err:%s" "$value" >&2; exit 23`,
		},
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code, err := executor.Execute(t.Context(), invocation, ProcessIO{
		Stdin:  strings.NewReader("literal value\n"),
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		t.Fatal(err)
	}
	if code != 23 {
		t.Fatalf("exit code = %d, want 23", code)
	}
	if stdout.String() != "out:literal value" {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if stderr.String() != "err:literal value" {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestExecutorMapsSignalStatus(t *testing.T) {
	executor := testExecutor(t, 100*time.Millisecond)
	invocation := &Invocation{
		Mode: ExecutionNonInteractive,
		Args: []string{"-c", `kill -TERM $$`},
	}

	code, err := executor.Execute(t.Context(), invocation, ProcessIO{})
	if err != nil {
		t.Fatal(err)
	}
	if code != 128+int(syscall.SIGTERM) {
		t.Fatalf("exit code = %d, want %d", code, 128+int(syscall.SIGTERM))
	}
}

func TestExecutorPreservesRepeatedFastExitStatus(t *testing.T) {
	executor := testExecutor(t, 100*time.Millisecond)

	for range 25 {
		invocation := &Invocation{
			Mode: ExecutionNonInteractive,
			Args: []string{"-c", "exit 17"},
		}
		code, err := executor.Execute(t.Context(), invocation, ProcessIO{})
		if err != nil {
			t.Fatal(err)
		}
		if code != 17 {
			t.Fatalf("exit code = %d, want 17", code)
		}
	}
}

func TestExecutorWaitsForSandboxNamespaceTeardown(t *testing.T) {
	executor := testExecutor(t, 100*time.Millisecond)
	marker := filepath.Join(t.TempDir(), "sandbox-exited")
	finalizing := 0

	code, err := executor.Execute(t.Context(), &Invocation{
		Mode: ExecutionNonInteractive,
		Args: []string{
			"--fake-delayed-sandbox-exit",
			marker,
		},
	}, ProcessIO{
		NotifyFinalizing: func() {
			finalizing++
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if finalizing != 1 {
		t.Fatalf("finalizing notifications = %d, want 1", finalizing)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("sandbox exit marker is unavailable after Execute: %v", err)
	}
}

func TestExecutorOmitsFinalizingNotificationAfterCompleteTeardown(
	t *testing.T,
) {
	executor := testExecutor(t, 100*time.Millisecond)
	finalizing := 0

	code, err := executor.Execute(t.Context(), &Invocation{
		Mode: ExecutionNonInteractive,
		Args: []string{"-c", "exit 0"},
	}, ProcessIO{
		NotifyFinalizing: func() {
			finalizing++
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if finalizing != 0 {
		t.Fatalf("finalizing notifications = %d, want 0", finalizing)
	}
}

func TestExecutorClosesInvocationWhenExecutorIsClosed(t *testing.T) {
	descriptor, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	invocation := &Invocation{
		Mode:       ExecutionNonInteractive,
		Args:       []string{"unused"},
		ExtraFiles: []*os.File{descriptor},
	}
	executor := testExecutor(t, 100*time.Millisecond)
	if err := executor.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := executor.Execute(t.Context(), invocation, ProcessIO{}); err == nil {
		t.Fatal("closed executor unexpectedly started")
	}
	if _, err := descriptor.Stat(); err == nil {
		t.Fatal("renderer-owned descriptor remains open after start failure")
	}
}

func TestExecutorRejectsNilInvocationDescriptor(t *testing.T) {
	executor := testExecutor(t, 100*time.Millisecond)
	invocation := &Invocation{
		Mode:       ExecutionNonInteractive,
		Args:       []string{"-c", "exit 0"},
		ExtraFiles: []*os.File{nil},
	}

	code, err := executor.Execute(t.Context(), invocation, ProcessIO{})
	if err == nil || !strings.Contains(err.Error(), "descriptor is nil") {
		t.Fatalf("nil descriptor result: code=%d error=%v", code, err)
	}
	if code != 1 {
		t.Fatalf("nil descriptor exit code = %d, want 1", code)
	}
}

func TestExecutorAbortsRetryMarkerWhenInvocationDuplicationFails(
	t *testing.T,
) {
	executor := testExecutor(t, 100*time.Millisecond)
	invocation := &Invocation{
		Mode:                   ExecutionNonInteractive,
		Args:                   []string{"--", "/bin/true"},
		ExtraFiles:             []*os.File{nil},
		payloadArgIndex:        1,
		allowOverlayReuseRetry: true,
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	started := time.Now()
	code, err := executor.Execute(ctx, invocation, ProcessIO{})
	if err == nil {
		t.Fatal("nil invocation descriptor was accepted")
	}
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf(
			"retry marker remained open after duplication failure for %s",
			elapsed,
		)
	}
}

func TestExecutorPrependsDescriptorOptionWithoutParsingArgumentValues(
	t *testing.T,
) {
	plan := validPlan()
	plan.Environment = append(plan.Environment, EnvironmentVariable{
		Name:  "BOUNDARY_VALUE",
		Value: "--",
	})
	sources := rendererSources(t, plan)
	invocation, err := Render(plan, sources)
	if err != nil {
		t.Fatal(err)
	}
	originalArgs := append([]string(nil), invocation.Args...)
	executableFD := childExtraFileBaseFD + len(invocation.ExtraFiles)

	fake, err := filepath.Abs("testdata/fake-bwrap-argv.sh")
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(
		ExecutorOptions{Executable: fake},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := executor.Close(); err != nil {
			t.Error(err)
		}
	})

	var output bytes.Buffer
	code, err := executor.Execute(t.Context(), invocation, ProcessIO{
		Stdout: &output,
	})
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}

	want := append(
		[]string{
			"--json-status-fd", strconv.Itoa(executableFD),
			"--block-fd", strconv.Itoa(executableFD + 1),
		},
		originalArgs...,
	)
	got := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
	if !slices.Equal(got, want) {
		t.Fatalf("executed args:\n got %q\nwant %q", got, want)
	}
}

func TestConfigureNonInteractiveCreatesDetachedProcessGroup(t *testing.T) {
	command := exec.Command("unused")
	input := strings.NewReader("input")
	var output bytes.Buffer
	var errorOutput bytes.Buffer

	configureNonInteractive(command, ProcessIO{
		Stdin:  input,
		Stdout: &output,
		Stderr: &errorOutput,
	})

	if command.Stdin != input ||
		command.Stdout != &output ||
		command.Stderr != &errorOutput {
		t.Fatal("noninteractive streams were not preserved")
	}
	if command.SysProcAttr == nil || !command.SysProcAttr.Setsid {
		t.Fatalf("noninteractive process attributes = %#v", command.SysProcAttr)
	}
	if command.SysProcAttr.Setpgid {
		t.Fatal("noninteractive process uses a pre-exec process group instead of a session")
	}
}

func TestConfigureNonInteractiveDoesNotConsumeTerminalInput(t *testing.T) {
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
	if _, err := master.Write([]byte("terminal-secret\n")); err != nil {
		t.Fatal(err)
	}

	command := exec.Command(
		"sh",
		"-c",
		`if IFS= read -r value; then printf 'consumed:%s' "$value"; else printf eof; fi`,
	)
	var output bytes.Buffer
	configureNonInteractive(command, ProcessIO{
		Stdin:  terminal,
		Stdout: &output,
		Stderr: io.Discard,
	})
	if command.Stdin != nil {
		t.Fatal("noninteractive terminal stdin was not replaced by /dev/null")
	}
	if err := command.Run(); err != nil {
		t.Fatal(err)
	}
	if output.String() != "eof" {
		t.Fatalf("noninteractive command output = %q, want eof", output.String())
	}
}

func TestConfigureDirectTerminalAllowsRedirectedOutput(t *testing.T) {
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

	command := exec.Command("unused")
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	err = configureDirectTerminal(command, ProcessIO{
		Stdin:  terminal,
		Stdout: &output,
		Stderr: &errorOutput,
	})
	if err != nil {
		t.Fatal(err)
	}
	if command.Stdin != terminal ||
		command.Stdout != &output ||
		command.Stderr != &errorOutput {
		t.Fatal("direct-terminal streams were not preserved")
	}
}

func TestConfigureDirectTerminalRejectsNonterminalInput(t *testing.T) {
	command := exec.Command("unused")
	err := configureDirectTerminal(command, ProcessIO{
		Stdin:  strings.NewReader("input"),
		Stdout: io.Discard,
		Stderr: io.Discard,
	})
	if err == nil || !strings.Contains(err.Error(), "stdin must be a terminal") {
		t.Fatalf("direct-terminal error = %v", err)
	}
}

func TestExecutorCancellationKillsProcessGroup(t *testing.T) {
	executor := testExecutor(t, 40*time.Millisecond)
	pidPath := filepath.Join(t.TempDir(), "descendant.pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	invocation := &Invocation{
		Mode: ExecutionNonInteractive,
		Args: []string{
			"-c",
			`trap '' TERM; sh -c 'trap "" TERM; exec sleep 30' & printf '%s\n' "$!" > "$1"; wait`,
			"toby-cancel-test",
			pidPath,
		},
	}
	result := make(chan struct {
		code int
		err  error
	}, 1)
	go func() {
		code, err := executor.Execute(ctx, invocation, ProcessIO{})
		result <- struct {
			code int
			err  error
		}{code: code, err: err}
	}()

	descendant := waitForPIDFile(t, pidPath)
	cancel()

	select {
	case got := <-result:
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("cancellation error = %v", got.err)
		}
		if got.code != 128+int(syscall.SIGKILL) {
			t.Fatalf(
				"cancellation exit code = %d, want %d",
				got.code,
				128+int(syscall.SIGKILL),
			)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled executor did not return")
	}

	waitForProcessGone(t, descendant)
}

func TestExecutorManagedPTYStreamsInputAndMergedOutput(t *testing.T) {
	executor := testExecutor(t, 100*time.Millisecond)
	invocation := &Invocation{
		Mode: ExecutionManagedPTY,
		Args: []string{
			"-c",
			`IFS= read -r value; printf '<%s>' "$value"; printf 'stderr-marker' >&2; exit 7`,
		},
	}
	var output bytes.Buffer

	code, err := executor.Execute(t.Context(), invocation, ProcessIO{
		Stdin:  strings.NewReader("pty-value\n"),
		Stdout: &output,
	})
	if err != nil {
		t.Fatal(err)
	}
	if code != 7 {
		t.Fatalf("exit code = %d, want 7", code)
	}
	if !strings.Contains(output.String(), "<pty-value>") ||
		!strings.Contains(output.String(), "stderr-marker") {
		t.Fatalf("managed PTY output = %q", output.String())
	}
}

func TestExecutorManagedPTYTerminatesOnOutputFailure(t *testing.T) {
	executor := testExecutor(t, 20*time.Millisecond)
	sentinel := errors.New("managed output failed")
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	code, err := executor.Execute(ctx, &Invocation{
		Mode: ExecutionManagedPTY,
		Args: []string{
			"-c",
			`while :; do printf 'managed-output'; done`,
		},
	}, ProcessIO{
		Stdout: errorWriter{err: sentinel},
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("managed output result: code=%d error=%v", code, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("managed output failure waited for context: %v", err)
	}
}

func TestExecutorManagedPTYTerminatesOnInputFailure(t *testing.T) {
	executor := testExecutor(t, 20*time.Millisecond)
	sentinel := errors.New("managed input failed")
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	code, err := executor.Execute(ctx, &Invocation{
		Mode: ExecutionManagedPTY,
		Args: []string{
			"-c",
			`IFS= read -r value; printf 'unexpected:%s' "$value"`,
		},
	}, ProcessIO{
		Stdin:  errorReader{err: sentinel},
		Stdout: io.Discard,
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("managed input result: code=%d error=%v", code, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("managed input failure waited for context: %v", err)
	}
}

func TestExecutorManagedPTYPreservesHostEndpointClosureErrors(t *testing.T) {
	for _, test := range []struct {
		name   string
		stream string
		err    error
	}{
		{name: "output-eio", stream: "output", err: syscall.EIO},
		{name: "output-closed", stream: "output", err: os.ErrClosed},
		{name: "input-eio", stream: "input", err: syscall.EIO},
		{name: "input-closed", stream: "input", err: os.ErrClosed},
	} {
		t.Run(test.name, func(t *testing.T) {
			executor := testExecutor(t, 20*time.Millisecond)
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()

			streams := ProcessIO{Stdout: io.Discard}
			command := `IFS= read -r value`
			if test.stream == "output" {
				streams.Stdout = errorWriter{err: test.err}
				command = `while :; do printf 'managed-output'; done`
			} else {
				streams.Stdin = errorReader{err: test.err}
			}

			code, err := executor.Execute(ctx, &Invocation{
				Mode: ExecutionManagedPTY,
				Args: []string{"-c", command},
			}, streams)
			if !errors.Is(err, test.err) {
				t.Fatalf(
					"managed %s result: code=%d error=%v, want %v",
					test.stream,
					code,
					err,
					test.err,
				)
			}
			if errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf(
					"managed %s failure waited for context: %v",
					test.stream,
					err,
				)
			}
		})
	}
}

func TestManagedPTYNormalizesOnlyMasterClosureErrors(t *testing.T) {
	for _, test := range []struct {
		name    string
		err     error
		run     func(error) error
		wantNil bool
	}{
		{
			name:    "output-master-read-eio",
			err:     syscall.EIO,
			wantNil: true,
			run: func(streamErr error) error {
				return pumpManagedPTYOutput(
					errorReader{err: streamErr},
					io.Discard,
				)
			},
		},
		{
			name: "output-master-read-closed",
			err:  os.ErrClosed,
			run: func(streamErr error) error {
				return pumpManagedPTYOutput(
					errorReader{err: streamErr},
					io.Discard,
				)
			},
		},
		{
			name:    "input-master-write-eio",
			err:     syscall.EIO,
			wantNil: true,
			run: func(streamErr error) error {
				return pumpManagedPTYInput(
					errorWriter{err: streamErr},
					strings.NewReader("input"),
				)
			},
		},
		{
			name:    "input-master-write-closed",
			err:     os.ErrClosed,
			wantNil: true,
			run: func(streamErr error) error {
				return pumpManagedPTYInput(
					errorWriter{err: streamErr},
					strings.NewReader("input"),
				)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.run(test.err)
			if test.wantNil {
				if err != nil {
					t.Fatalf("master closure error = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, test.err) {
				t.Fatalf("master read error = %v, want %v", err, test.err)
			}
		})
	}
}

func TestManagedPTYInputClosePreservesOnlyOperationalFailure(t *testing.T) {
	operationErr := errors.New("input pump failed")
	cleanupErr := errors.New("reader close failed")

	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "successful input", err: nil},
		{name: "failed input", err: operationErr},
	} {
		t.Run(test.name, func(t *testing.T) {
			done := make(chan struct{})
			close(done)
			input := &managedPTYInput{
				reader: cancelReaderStub{closeErr: cleanupErr},
				done:   done,
				err:    test.err,
			}

			err := input.Close()
			if !errors.Is(err, test.err) {
				t.Fatalf("input close error = %v, want %v", err, test.err)
			}
			if errors.Is(err, cleanupErr) {
				t.Fatalf("input close exposed cleanup error: %v", err)
			}
		})
	}
}

type cancelReaderStub struct {
	closeErr error
}

func (cancelReaderStub) Read([]byte) (int, error) {
	return 0, io.EOF
}

func (cancelReaderStub) Cancel() bool {
	return true
}

func (r cancelReaderStub) Close() error {
	return r.closeErr
}

type errorReader struct {
	err error
}

func (r errorReader) Read([]byte) (int, error) {
	return 0, r.err
}

func TestExecutorForwardsPreExecBubblewrapStderrOnce(t *testing.T) {
	executor := testExecutor(t, 100*time.Millisecond)
	attempts := filepath.Join(t.TempDir(), "attempts")
	invocation := &Invocation{
		Mode: ExecutionNonInteractive,
		Args: []string{
			"--fake-pre-exec-fail",
			attempts,
		},
	}
	var stderr bytes.Buffer

	code, err := executor.Execute(t.Context(), invocation, ProcessIO{
		Stderr: &stderr,
	})
	if err == nil || !strings.Contains(err.Error(), "before payload exec") {
		t.Fatalf("pre-exec failure error = %v", err)
	}
	if strings.Contains(err.Error(), "synthetic setup failure") {
		t.Fatalf("Bubblewrap diagnostic was copied into Toby's error: %v", err)
	}
	if code != 1 {
		t.Fatalf("pre-exec failure exit code = %d, want 1", code)
	}
	content, readErr := os.ReadFile(attempts)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(content) != "x" {
		t.Fatalf("Bubblewrap attempts = %q, want one", content)
	}
	if got, want := stderr.String(), "bwrap: synthetic setup failure\n"; got != want {
		t.Fatalf("Bubblewrap stderr = %q, want %q", got, want)
	}
}

func TestExecutorRetriesProvenOverlayReuseFailureBeforePayload(
	t *testing.T,
) {
	executor := testExecutor(t, 100*time.Millisecond)
	state := filepath.Join(t.TempDir(), "attempted")
	payload := filepath.Join(t.TempDir(), "payload")
	invocation := &Invocation{
		Mode:                   ExecutionNonInteractive,
		allowOverlayReuseRetry: true,
		payloadArgIndex:        3,
		Args: []string{
			"--fake-retry-state", state,
			"--",
			"/bin/sh", "-c",
			`printf payload >>"$1"`,
			"fake-payload",
			payload,
		},
	}
	var stderr bytes.Buffer

	code, err := executor.Execute(t.Context(), invocation, ProcessIO{
		Stderr: &stderr,
	})
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	content, err := os.ReadFile(payload)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "payload" {
		t.Fatalf("payload side effects = %q, want exactly one", content)
	}
	if stderr.Len() != 0 {
		t.Fatalf(
			"successful retry leaked Bubblewrap diagnostics: %q",
			stderr.String(),
		)
	}
}

func TestExecutorBoundsRepeatedPrePayloadFailures(t *testing.T) {
	executable, err := filepath.Abs("testdata/fake-bwrap-exec.sh")
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(
		ExecutorOptions{
			Executable:                executable,
			TerminationGrace:          100 * time.Millisecond,
			OverlayReuseRetryTimeout:  100 * time.Millisecond,
			OverlayReuseRetryInterval: time.Millisecond,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := executor.Close(); err != nil {
			t.Error(err)
		}
	})

	attempts := filepath.Join(t.TempDir(), "attempts")
	invocation := &Invocation{
		Mode:                   ExecutionNonInteractive,
		allowOverlayReuseRetry: true,
		payloadArgIndex:        3,
		Args: []string{
			"--fake-always-pre-exec-fail", attempts,
			"--",
			"/bin/true",
		},
	}
	var stderr bytes.Buffer

	started := time.Now()
	code, err := executor.Execute(t.Context(), invocation, ProcessIO{
		Stderr: &stderr,
	})
	elapsed := time.Since(started)
	if err == nil || !strings.Contains(err.Error(), "before payload exec") {
		t.Fatalf("bounded retry error = %v", err)
	}
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	content, readErr := os.ReadFile(attempts)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(content) < 2 || len(content) > 1000 {
		t.Fatalf("bounded attempt count = %d", len(content))
	}
	if elapsed > 2*time.Second {
		t.Fatalf("bounded retry elapsed time = %s", elapsed)
	}
	if got, want := stderr.String(), "bwrap: transient overlay failure\n"; got != want {
		t.Fatalf("final retry diagnostics = %q, want %q", got, want)
	}
}

func TestExecutorDoesNotRetryPayloadAfterReadyMarker(t *testing.T) {
	executor := testExecutor(t, 100*time.Millisecond)
	state := filepath.Join(t.TempDir(), "attempted")
	if err := os.WriteFile(state, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(t.TempDir(), "payload")
	invocation := &Invocation{
		Mode:                   ExecutionNonInteractive,
		allowOverlayReuseRetry: true,
		payloadArgIndex:        3,
		Args: []string{
			"--fake-retry-state", state,
			"--",
			"/bin/sh", "-c",
			`printf payload >>"$1"; exit 1`,
			"fake-payload",
			payload,
		},
	}

	code, err := executor.Execute(t.Context(), invocation, ProcessIO{})
	if err != nil {
		t.Fatal(err)
	}
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	content, err := os.ReadFile(payload)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "payload" {
		t.Fatalf("payload side effects = %q, want exactly one", content)
	}
}

func TestForegroundSignalTargetsPayloadWithoutStoppingMonitor(t *testing.T) {
	executor := testExecutor(t, 100*time.Millisecond)
	helper, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	ready := filepath.Join(root, "ready")
	started := filepath.Join(root, "shutdown-started")
	finished := filepath.Join(root, "shutdown-finished")
	invocation := &Invocation{
		Mode:                   ExecutionNonInteractive,
		allowOverlayReuseRetry: true,
		payloadArgIndex:        2,
		Args: []string{
			"--fake-payload-helper",
			helper,
			"/bin/sh", "-c",
			`trap 'touch "$2"; sleep 0.1; touch "$3"; exit 0' INT; touch "$1"; while :; do sleep 0.05; done`,
			"payload",
			ready,
			started,
			finished,
		},
	}
	registered := make(chan func(syscall.Signal) error, 1)
	result := make(chan struct {
		code int
		err  error
	}, 1)
	go func() {
		code, err := executor.Execute(t.Context(), invocation, ProcessIO{
			RegisterSignalHandler: func(
				handler func(syscall.Signal) error,
			) func() {
				registered <- handler
				return func() {}
			},
		})
		result <- struct {
			code int
			err  error
		}{code: code, err: err}
	}()

	var signal func(syscall.Signal) error
	select {
	case signal = <-registered:
	case <-time.After(time.Second):
		t.Fatal("foreground signal handler was not registered")
	}
	waitForTestFile(t, ready)
	if err := signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-result:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.code != 0 {
			t.Fatalf("exit code = %d, want 0", got.code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("gracefully interrupted payload did not exit")
	}
	if _, err := os.Stat(started); err != nil {
		t.Fatalf("payload shutdown did not start: %v", err)
	}
	if _, err := os.Stat(finished); err != nil {
		t.Fatalf("payload shutdown did not finish: %v", err)
	}
}

func TestExecutionAttemptClassifiesReadyPayloadWithoutExitEvent(t *testing.T) {
	attempt := executionAttempt{
		code: 1,
		status: bubblewrapStatus{
			hasChildPID: true,
		},
		payloadStarted: true,
	}

	if attempt.canRetry(t.Context()) {
		t.Fatal("ready payload remained retryable")
	}
	code, err := attempt.result()
	if err != nil {
		t.Fatal(err)
	}
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
}

func TestExecutorPropagatesMonitorSignalAfterPayloadReady(t *testing.T) {
	executor := testExecutor(t, 100*time.Millisecond)
	invocation := &Invocation{
		Mode:                   ExecutionManagedPTY,
		allowOverlayReuseRetry: true,
		payloadArgIndex:        2,
		Args: []string{
			"--fake-ready-monitor-interrupt",
			"--",
			"/bin/sh", "-c",
			"printf payload",
		},
	}
	var output bytes.Buffer

	code, err := executor.Execute(t.Context(), invocation, ProcessIO{
		Stdout: &output,
	})
	if err != nil {
		t.Fatal(err)
	}
	if code != 130 {
		t.Fatalf("exit code = %d, want 130", code)
	}
	if got := output.String(); !strings.Contains(got, "payload") {
		t.Fatalf("payload output = %q, want payload", got)
	}
}

func TestExecutorForwardsPayloadStderrVerbatim(t *testing.T) {
	executor := testExecutor(t, 100*time.Millisecond)
	invocation := &Invocation{
		Mode: ExecutionNonInteractive,
		Args: []string{
			"-c",
			`printf '<3>bwrap: payload-owned\n<3>bwrap: second\n' >&2`,
		},
	}
	var stderr bytes.Buffer

	code, err := executor.Execute(t.Context(), invocation, ProcessIO{
		Stderr: &stderr,
	})
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	want := "<3>bwrap: payload-owned\n<3>bwrap: second\n"
	if got := stderr.String(); got != want {
		t.Fatalf("payload stderr = %q, want %q", got, want)
	}
}

func testExecutor(t *testing.T, grace time.Duration) *Executor {
	t.Helper()

	executable, err := filepath.Abs("testdata/fake-bwrap-exec.sh")
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(
		ExecutorOptions{
			Executable:       executable,
			TerminationGrace: grace,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := executor.Close(); err != nil {
			t.Error(err)
		}
	})

	return executor
}

func waitForPIDFile(t *testing.T, path string) int {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if parseErr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("process PID was not written to %s", path)

	return 0
}

func waitForTestFile(t *testing.T, path string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("file was not created: %s", path)
}

func waitForProcessGone(t *testing.T, pid int) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil {
			t.Fatalf("inspect descendant %d: %v", pid, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("descendant process %d survived cancellation", pid)
}
