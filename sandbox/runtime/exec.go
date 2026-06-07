package runtime

// Docker-exec backend: runs commands in the long-lived Run container via the raw
// moby exec API (testcontainers' Exec wrapper attaches no stdin and blocks until
// exit, so it cannot drive an interactive tool). A foreground launch always runs
// under a container PTY so the tool line-buffers and flushes its output and
// renders as if attached to a terminal; when the host itself is a terminal that
// PTY is additionally wired to it (raw mode, resize, PTY-delivered signals), and
// when it is not — a systemd service, a redirected run — the PTY stream is copied
// straight through so the output still reaches the host stdout. Non-interactive
// commands run without a PTY and capture or discard output, returning the exit
// code.

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	dstdcopy "github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"
	"golang.org/x/term"

	sandboxapi "petris.dev/toby/sandbox"
)

// defaultCols and defaultRows seed a headless foreground PTY (no host terminal to
// measure) so the tool sees a plausible terminal size instead of zero.
const (
	defaultCols = 80
	defaultRows = 24
)

// ExecSpec describes one command to run in the Run container.
type ExecSpec struct {
	Argv        []string
	Env         []string // KEY=VALUE, the host-held environment
	User        string   // "uid:gid"; empty means the container default
	WorkingDir  string
	Interactive bool // foreground: attach stdin and run under a container PTY
	HideOutput  bool // discard stdout/stderr
	Managed     bool // foreground: use Toby's managed terminal (shadow + approval modal)
	// RegisterPrompter, when set, registers (and on exit clears) the foreground's
	// approval prompter so host-side services can ask the user during an interactive
	// run.
	RegisterPrompter func(sandboxapi.ApprovalPrompter)
}

// Exec runs spec in the Run container and returns its exit code.
func (s *instance) Exec(ctx context.Context, spec ExecSpec) (int, error) {
	cli, err := s.containers.Client(ctx)
	if err != nil {
		return 1, err
	}
	id := s.runContainerID()
	if id == "" {
		return 1, fmt.Errorf("run container is not started")
	}

	// A foreground tool always runs under a container PTY: without one its stdout
	// is a pipe, so it block-buffers and a long-running tool's output never reaches
	// a non-terminal host (a systemd journal, a log file). The host being a terminal
	// only decides how the PTY is driven, not whether one is allocated.
	tty := spec.Interactive
	hostTerminal := stdinIsTerminal() && stdoutIsTerminal()

	createOpts := client.ExecCreateOptions{
		User:         spec.User,
		TTY:          tty,
		AttachStdin:  spec.Interactive,
		AttachStdout: true,
		AttachStderr: true,
		Env:          spec.Env,
		WorkingDir:   spec.WorkingDir,
		Cmd:          spec.Argv,
	}
	attachOpts := client.ExecAttachOptions{TTY: tty}
	// Seed the PTY with its size at creation so the tool reads a correct terminal size
	// from its very first byte instead of Docker's 80x24 default. With a host terminal
	// that is the real terminal size — matching the host-side emulator, so a tool that
	// probes its size at startup gets a consistent answer and does not stall; without
	// one there is no size to measure, so a sane default is used.
	if tty {
		cols, rows := defaultCols, defaultRows
		if hostTerminal {
			if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 && h > 0 {
				cols, rows = w, h
			}
		}
		createOpts.ConsoleSize = client.ConsoleSize{Height: uint(rows), Width: uint(cols)}
		attachOpts.ConsoleSize = client.ConsoleSize{Height: uint(rows), Width: uint(cols)}
	}
	created, err := cli.ExecCreate(ctx, id, createOpts)
	if err != nil {
		return 1, err
	}

	attach, err := cli.ExecAttach(ctx, created.ID, attachOpts)
	if err != nil {
		return 1, err
	}
	defer attach.Close()

	// Unblock the stream copy if the context is cancelled; the exec process is
	// reaped when the host later stops the container during teardown.
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-streamCtx.Done()
		attach.Close()
	}()

	switch {
	case tty && hostTerminal && spec.Managed:
		s.runExecForeground(ctx, cli, created.ID, attach.HijackedResponse, spec.RegisterPrompter)
	case tty && hostTerminal:
		// Managed terminal disabled: plain raw passthrough, no shadow or approval modal.
		s.runExecTTY(ctx, cli, created.ID, attach.HijackedResponse)
	case tty:
		runExecHeadlessTTY(attach.HijackedResponse, spec)
	default:
		s.runExecPlain(attach.HijackedResponse, spec)
	}

	code, err := execExitCode(ctx, cli, created.ID)
	if err != nil {
		if ctx.Err() != nil {
			return 130, ctx.Err()
		}
		return 1, err
	}
	if ctx.Err() != nil {
		return 130, ctx.Err()
	}
	return code, nil
}

// runExecTTY wires the host terminal to the exec stream in raw mode: SIGWINCH
// resizes the exec PTY and Ctrl-C reaches the process as a PTY byte (so no signal
// forwarding is needed).
func (s *instance) runExecTTY(ctx context.Context, cli *testcontainers.DockerClient, execID string, attach client.HijackedResponse) {
	if state, err := term.MakeRaw(int(os.Stdin.Fd())); err == nil {
		defer func() { _ = term.Restore(int(os.Stdin.Fd()), state) }()
	}
	resizeExec(ctx, cli, execID)
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	go func() {
		for range winch {
			resizeExec(ctx, cli, execID)
		}
	}()

	outDone := make(chan struct{})
	go func() {
		defer close(outDone)
		_, _ = io.Copy(os.Stdout, attach.Reader)
	}()
	go func() {
		_, _ = io.Copy(attach.Conn, os.Stdin)
		_ = attach.CloseWrite()
	}()
	<-outDone
}

// runExecHeadlessTTY streams a foreground exec that runs under a container PTY but
// has no host terminal (a service or a redirected run). The PTY merges the tool's
// stdout and stderr into one stream — there is nothing to demultiplex — and makes
// the tool line-buffer and flush, so io.Copy carries its output to the host stdout
// (and on to the journal) promptly. Host stdin is forwarded so a tool that reads it
// still works, but its write half is left open: there is no terminal to signal EOF,
// and closing it would feed a spurious end-of-input to a long-running server. Raw
// mode, resize, and signal forwarding are all skipped — there is no host terminal to
// drive them.
func runExecHeadlessTTY(attach client.HijackedResponse, spec ExecSpec) {
	if spec.Interactive {
		go func() {
			_, _ = io.Copy(attach.Conn, os.Stdin)
		}()
	}
	out := io.Writer(os.Stdout)
	if spec.HideOutput {
		out = io.Discard
	}
	_, _ = io.Copy(out, attach.Reader)
}

// runExecPlain streams a non-interactive exec: stdin is forwarded only for
// foreground commands, output is demultiplexed to the host stdio or discarded.
func (s *instance) runExecPlain(attach client.HijackedResponse, spec ExecSpec) {
	if spec.Interactive {
		go func() {
			_, _ = io.Copy(attach.Conn, os.Stdin)
			_ = attach.CloseWrite()
		}()
	}
	outW, errW := io.Writer(os.Stdout), io.Writer(os.Stderr)
	if spec.HideOutput {
		outW, errW = io.Discard, io.Discard
	}
	_, _ = dstdcopy.StdCopy(outW, errW, attach.Reader)
}

// execExitCode polls until the exec process leaves the running state and returns
// its exit code.
func execExitCode(ctx context.Context, cli *testcontainers.DockerClient, execID string) (int, error) {
	for {
		inspect, err := cli.ExecInspect(ctx, execID, client.ExecInspectOptions{})
		if err != nil {
			return 1, err
		}
		if !inspect.Running {
			return inspect.ExitCode, nil
		}
		select {
		case <-ctx.Done():
			return 1, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func resizeExec(ctx context.Context, cli *testcontainers.DockerClient, execID string) {
	width, height, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		return
	}
	_, _ = cli.ExecResize(ctx, execID, client.ExecResizeOptions{Height: uint(height), Width: uint(width)})
}

func (s *instance) runContainerID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runContainer == nil {
		return ""
	}
	return s.runContainer.GetContainerID()
}
