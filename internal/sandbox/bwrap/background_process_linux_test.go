//go:build linux

package bwrap

// Verifies background-only validation, descriptor ownership, stdio support,
// exact signaling, and reap-before-Done behavior.

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"

	sandboxapi "petris.dev/toby/internal/sandbox"
)

type blockingBackgroundReader struct {
	release <-chan struct{}
}

func (r blockingBackgroundReader) Read([]byte) (int, error) {
	<-r.release
	return 0, io.EOF
}

type blockingBackgroundWriter struct {
	release <-chan struct{}
}

func (w blockingBackgroundWriter) Write(value []byte) (int, error) {
	<-w.release
	return len(value), nil
}

func TestConfigureBackgroundCommandDetachesFromTerminal(t *testing.T) {
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
	command.Stdin = terminal

	configureBackgroundCommand(command)

	if command.SysProcAttr == nil || !command.SysProcAttr.Setsid {
		t.Fatalf("background process attributes = %#v", command.SysProcAttr)
	}
	if command.SysProcAttr.Pdeathsig != syscall.SIGKILL {
		t.Fatalf(
			"background parent-death signal = %s, want %s",
			command.SysProcAttr.Pdeathsig,
			syscall.SIGKILL,
		)
	}
	if command.Stdin != nil {
		t.Fatal("background terminal stdin was not replaced by /dev/null")
	}
}

func TestStartBackgroundConsumesInvocationOnEveryReturn(t *testing.T) {
	descriptor, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	invocation := &Invocation{
		Args:       backgroundTestArgs("sleep", "30"),
		ExtraFiles: []*os.File{descriptor},
		Mode:       ExecutionNonInteractive,
	}

	var executor *Executor
	if _, err := executor.StartBackground(
		t.Context(),
		invocation,
		ProcessIO{},
	); err == nil {
		t.Fatal("nil executor unexpectedly started a background process")
	}
	if _, err := descriptor.Stat(); err == nil {
		t.Fatal("rejected invocation descriptor remains open")
	}
}

func TestStartBackgroundRequiresFixedNoninteractivePolicy(t *testing.T) {
	executor := backgroundTestExecutor(t)

	tests := []struct {
		name       string
		invocation *Invocation
		streams    ProcessIO
	}{
		{
			name: "interactive",
			invocation: &Invocation{
				Args: backgroundTestArgs("sleep", "30"),
				Mode: ExecutionManagedPTY,
			},
		},
		{
			name: "missing parent death",
			invocation: &Invocation{
				Args: []string{
					"--unshare-user",
					"--uid", "1000",
					"--gid", "1000",
					"--unshare-pid",
					"--unshare-ipc",
					"--unshare-uts",
					"--",
					"sleep", "30",
				},
				Mode: ExecutionNonInteractive,
			},
		},
		{
			name: "approval prompter",
			invocation: &Invocation{
				Args: backgroundTestArgs("sleep", "30"),
				Mode: ExecutionNonInteractive,
			},
			streams: ProcessIO{
				RegisterPrompter: func(sandboxapi.ApprovalPrompter) {},
			},
		},
		{
			name: "payload as pid 1",
			invocation: &Invocation{
				Args: append(
					namespaceArgs(os.Getuid(), os.Getgid()),
					"--as-pid-1",
					"--",
					"sleep", "30",
				),
				Mode: ExecutionNonInteractive,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := executor.StartBackground(
				t.Context(),
				test.invocation,
				test.streams,
			); err == nil {
				t.Fatal("invalid background policy unexpectedly accepted")
			}
		})
	}
}

func TestStartBackgroundRejectsNonFileStreams(t *testing.T) {
	executor := backgroundTestExecutor(t)

	tests := []struct {
		name    string
		streams func(<-chan struct{}) ProcessIO
		want    string
	}{
		{
			name: "stdin",
			streams: func(release <-chan struct{}) ProcessIO {
				return ProcessIO{
					Stdin: blockingBackgroundReader{release: release},
				}
			},
			want: "stdin must be unset or hold a non-nil direct *os.File",
		},
		{
			name: "stdout",
			streams: func(release <-chan struct{}) ProcessIO {
				return ProcessIO{
					Stdout: blockingBackgroundWriter{release: release},
				}
			},
			want: "stdout must be unset or hold a non-nil direct *os.File",
		},
		{
			name: "stderr",
			streams: func(release <-chan struct{}) ProcessIO {
				return ProcessIO{
					Stderr: blockingBackgroundWriter{release: release},
				}
			},
			want: "stderr must be unset or hold a non-nil direct *os.File",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			release := make(chan struct{})
			process, err := executor.StartBackground(
				t.Context(),
				&Invocation{
					Args: backgroundTestArgs(
						"/bin/sh",
						"-c",
						`printf stdout; printf stderr >&2; IFS= read -r _`,
					),
					Mode: ExecutionNonInteractive,
				},
				test.streams(release),
			)
			close(release)
			if process != nil {
				_ = process.Kill(t.Context())
				waitForBackgroundDone(t, process)
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("non-file %s result = %v", test.name, err)
			}
		})
	}
}

func TestStartBackgroundRejectsTypedNilFileStream(t *testing.T) {
	executor := backgroundTestExecutor(t)
	var input *os.File

	process, err := executor.StartBackground(
		t.Context(),
		&Invocation{
			Args: backgroundTestArgs("/bin/true"),
			Mode: ExecutionNonInteractive,
		},
		ProcessIO{Stdin: input},
	)
	if process != nil {
		_ = process.Kill(t.Context())
		waitForBackgroundDone(t, process)
	}
	if err == nil ||
		!strings.Contains(
			err.Error(),
			"stdin must be unset or hold a non-nil direct *os.File",
		) {
		t.Fatalf("typed-nil stdin result = %v", err)
	}
}

func TestBackgroundProcessSupportsStdioAndNaturalReap(t *testing.T) {
	executor := backgroundTestExecutor(t)
	root := t.TempDir()
	output := filepath.Join(root, "stdin")
	input, err := os.CreateTemp(root, "input-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := input.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := input.WriteString("stdio-value\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := input.Seek(0, 0); err != nil {
		t.Fatal(err)
	}

	invocation := &Invocation{
		Args: backgroundTestArgs(
			"/bin/sh",
			"-c",
			`IFS= read -r value; printf '%s' "$value" > "$1"; /bin/sleep 0.1`,
			"toby-background-stdio",
			output,
		),
		Mode: ExecutionNonInteractive,
	}

	process, err := executor.StartBackground(
		t.Context(),
		invocation,
		ProcessIO{Stdin: input},
	)
	if err != nil {
		t.Fatal(err)
	}
	waitForBackgroundDone(t, process)
	if err := process.Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := input.Stat(); err != nil {
		t.Fatalf("caller-owned stdin descriptor was closed: %v", err)
	}

	content, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "stdio-value" {
		t.Fatalf("background stdin output = %q", content)
	}
}

func TestBackgroundProcessGracefullySignalsPayloadAndReapsBeforeDone(
	t *testing.T,
) {
	executor := backgroundTestExecutor(t)
	root := t.TempDir()
	ready := filepath.Join(root, "ready")
	handled := filepath.Join(root, "handled")
	process, err := executor.StartBackground(
		t.Context(),
		&Invocation{
			Args: backgroundTestArgs(
				"/bin/sh",
				"-c",
				`trap ': > "$1"; exit 0' TERM; : > "$2"; while :; do /bin/sleep 0.1; done`,
				"toby-background-term",
				handled,
				ready,
			),
			Mode: ExecutionNonInteractive,
		},
		ProcessIO{},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = process.Kill(context.Background())
		waitForBackgroundDone(t, process)
	})

	monitorPIDFD, initPIDFD, payloadPIDFD := duplicateBackgroundTestPIDFDs(
		t,
		process,
	)
	defer unix.Close(monitorPIDFD)
	defer unix.Close(initPIDFD)
	defer unix.Close(payloadPIDFD)

	waitForBackgroundTestFile(t, ready)
	if err := process.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitForBackgroundDone(t, process)
	if !backgroundTestPidfdExited(t, monitorPIDFD) {
		t.Fatal("background monitor remains live after Done closed")
	}
	if !backgroundTestPidfdExited(t, initPIDFD) {
		t.Fatal("background init remains live after Done closed")
	}
	if !backgroundTestPidfdExited(t, payloadPIDFD) {
		t.Fatal("background payload remains live after Done closed")
	}
	if err := process.Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(handled); err != nil {
		t.Fatalf("payload TERM handler did not run: %v", err)
	}
}

func TestBackgroundProcessKillTearsDownFakeTopology(t *testing.T) {
	executor := backgroundTestExecutor(t)
	process, err := executor.StartBackground(
		t.Context(),
		&Invocation{
			Args: backgroundTestArgs("/bin/sleep", "30"),
			Mode: ExecutionNonInteractive,
		},
		ProcessIO{},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = process.Kill(context.Background())
		waitForBackgroundDone(t, process)
	})

	monitorPIDFD, initPIDFD, payloadPIDFD := duplicateBackgroundTestPIDFDs(
		t,
		process,
	)
	defer unix.Close(monitorPIDFD)
	defer unix.Close(initPIDFD)
	defer unix.Close(payloadPIDFD)

	if err := process.Kill(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitForBackgroundDone(t, process)
	if !backgroundTestPidfdExited(t, monitorPIDFD) {
		t.Fatal("forced background monitor remains live after Done closed")
	}
	if !backgroundTestPidfdExited(t, initPIDFD) {
		t.Fatal("forced background init remains live after Done closed")
	}
	if !backgroundTestPidfdExited(t, payloadPIDFD) {
		t.Fatal("forced background payload remains live after Done closed")
	}
	if process.Err() == nil {
		t.Fatal("forced process exit did not retain a wait error")
	}
}

func duplicateBackgroundTestPIDFDs(
	t *testing.T,
	process BackgroundProcess,
) (int, int, int) {
	t.Helper()

	concrete := process.(*backgroundProcess)
	concrete.mu.Lock()
	monitorPIDFD, monitorErr := unix.Dup(concrete.monitor.pidfd)
	initPIDFD, initErr := unix.Dup(concrete.init.pidfd)
	payloadPIDFD, payloadErr := unix.Dup(concrete.payload.pidfd)
	concrete.mu.Unlock()
	if monitorErr == nil && initErr == nil && payloadErr == nil {
		return monitorPIDFD, initPIDFD, payloadPIDFD
	}

	for _, descriptor := range []int{
		monitorPIDFD,
		initPIDFD,
		payloadPIDFD,
	} {
		if descriptor >= 0 {
			_ = unix.Close(descriptor)
		}
	}
	t.Fatal(errors.Join(monitorErr, initErr, payloadErr))

	return -1, -1, -1
}

func backgroundTestPidfdExited(t *testing.T, pidfd int) bool {
	t.Helper()

	poll := []unix.PollFd{{
		Fd:     int32(pidfd),
		Events: unix.POLLIN,
	}}
	count, err := unix.Poll(poll, 0)
	if err != nil {
		t.Fatal(err)
	}

	return count == 1 &&
		poll[0].Revents&(unix.POLLIN|unix.POLLHUP|unix.POLLERR) != 0
}

func TestBackgroundProcessSignalHonorsCanceledContext(t *testing.T) {
	executor := backgroundTestExecutor(t)
	process, err := executor.StartBackground(
		t.Context(),
		&Invocation{
			Args: backgroundTestArgs("sleep", "30"),
			Mode: ExecutionNonInteractive,
		},
		ProcessIO{},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = process.Stop(context.Background())
		waitForBackgroundDone(t, process)
	})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := process.Stop(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Stop with canceled context = %v", err)
	}

	select {
	case <-process.Done():
		t.Fatal("canceled signal context terminated background process")
	default:
	}
}

func waitForBackgroundTestFile(t *testing.T, path string) {
	t.Helper()

	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}

		select {
		case <-timer.C:
			t.Fatalf("background payload did not publish %s", path)
		case <-ticker.C:
		}
	}
}

func backgroundTestExecutor(t *testing.T) *Executor {
	t.Helper()

	path, err := filepath.Abs("testdata/fake-bwrap-background.sh")
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(
		ExecutorOptions{Executable: path},
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

func backgroundTestArgs(command ...string) []string {
	args := namespaceArgs(os.Getuid(), os.Getgid())
	args = append(args, "--")
	return append(args, command...)
}

func waitForBackgroundDone(
	t *testing.T,
	process BackgroundProcess,
) {
	t.Helper()

	select {
	case <-process.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("background process was not reaped")
	}
}
