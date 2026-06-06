package runtime

// Docker-exec backend: runs commands in the long-lived Run container via the raw
// moby exec API (testcontainers' Exec wrapper attaches no stdin and blocks until
// exit, so it cannot drive an interactive tool). Interactive launches wire the
// host terminal to the exec stream (raw mode, resize, PTY-delivered signals);
// non-interactive commands capture or discard output and return the exit code.

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
)

// ExecSpec describes one command to run in the Run container.
type ExecSpec struct {
	Argv        []string
	Env         []string // KEY=VALUE, the host-held environment
	User        string   // "uid:gid"; empty means the container default
	WorkingDir  string
	Interactive bool // foreground: attach stdin (and a TTY when the host is a terminal)
	HideOutput  bool // discard stdout/stderr
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

	tty := spec.Interactive && stdinIsTerminal() && stdoutIsTerminal()
	created, err := cli.ExecCreate(ctx, id, client.ExecCreateOptions{
		User:         spec.User,
		TTY:          tty,
		AttachStdin:  spec.Interactive,
		AttachStdout: true,
		AttachStderr: true,
		Env:          spec.Env,
		WorkingDir:   spec.WorkingDir,
		Cmd:          spec.Argv,
	})
	if err != nil {
		return 1, err
	}

	attach, err := cli.ExecAttach(ctx, created.ID, client.ExecAttachOptions{TTY: tty})
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

	if tty {
		s.runExecTTY(ctx, cli, created.ID, attach.HijackedResponse)
	} else {
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
