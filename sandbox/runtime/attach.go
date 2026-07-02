package runtime

// Client-side attach to a tool container's main process. The tool container's entry is
// `sandbox launch`, which execs the actual tool as PID 1; the client attaches to that
// process (not a docker exec) so its PTY drives the tool directly, with the same
// managed-terminal / approval-modal driver used before. Attach + start + wait replaces
// the old exec create/attach/inspect flow.

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	dcontainer "github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"
	"golang.org/x/term"

	sandboxapi "petris.dev/toby/sandbox"
)

// AttachOptions tunes the foreground attach.
type AttachOptions struct {
	Managed          bool
	RegisterPrompter func(sandboxapi.ApprovalPrompter)
}

// AttachAndRun attaches to the created tool container, starts it, drives its PTY, and
// returns the tool's exit code.
func AttachAndRun(ctx context.Context, cli *testcontainers.DockerClient, containerID string, opts AttachOptions) (int, error) {
	hostTerminal := stdinIsTerminal() && stdoutIsTerminal()

	attach, err := cli.ContainerAttach(ctx, containerID, client.ContainerAttachOptions{
		Stream: true, Stdin: true, Stdout: true, Stderr: true,
	})
	if err != nil {
		return 1, err
	}
	defer attach.Close()

	if _, err := cli.ContainerStart(ctx, containerID, client.ContainerStartOptions{}); err != nil {
		return 1, err
	}

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-streamCtx.Done()
		attach.Close()
	}()

	resize := func(cols, rows int) {
		if cols <= 0 || rows <= 0 {
			return
		}
		_, _ = cli.ContainerResize(ctx, containerID, client.ContainerResizeOptions{Height: uint(rows), Width: uint(cols)})
	}

	switch {
	case hostTerminal && opts.Managed:
		runExecForeground(ctx, resize, attach.HijackedResponse, opts.RegisterPrompter)
	case hostTerminal:
		runAttachTTY(ctx, resize, attach.HijackedResponse)
	default:
		runAttachHeadless(attach.HijackedResponse)
	}

	return containerExitCode(ctx, cli, containerID)
}

// runAttachTTY is a plain raw passthrough between the host terminal and the container
// PTY (managed terminal off) — raw mode, SIGWINCH resize, Ctrl-C reaches the tool as a
// PTY byte.
func runAttachTTY(ctx context.Context, resize func(cols, rows int), attach client.HijackedResponse) {
	if state, err := term.MakeRaw(int(os.Stdin.Fd())); err == nil {
		defer func() { _ = term.Restore(int(os.Stdin.Fd()), state) }()
	}
	if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		resize(w, h)
	}
	watchResize(ctx, resize)

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

// runAttachHeadless streams a container PTY with no host terminal (a service or a
// redirected run): forward stdin, copy the merged PTY output to stdout.
func runAttachHeadless(attach client.HijackedResponse) {
	go func() {
		_, _ = io.Copy(attach.Conn, os.Stdin)
	}()
	_, _ = io.Copy(os.Stdout, attach.Reader)
}

// watchResize resizes on SIGWINCH until the context ends.
func watchResize(ctx context.Context, resize func(cols, rows int)) {
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	go func() {
		defer signal.Stop(winch)
		for {
			select {
			case <-ctx.Done():
				return
			case <-winch:
				if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
					resize(w, h)
				}
			}
		}
	}()
}

// containerExitCode waits for the container to stop and returns its exit code.
func containerExitCode(ctx context.Context, cli *testcontainers.DockerClient, id string) (int, error) {
	result := cli.ContainerWait(ctx, id, client.ContainerWaitOptions{Condition: dcontainer.WaitConditionNotRunning})
	select {
	case res := <-result.Result:
		return int(res.StatusCode), nil
	case werr := <-result.Error:
		if ctx.Err() != nil {
			return 130, ctx.Err()
		}
		return 1, werr
	case <-ctx.Done():
		return 130, ctx.Err()
	}
}
