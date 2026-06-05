package docker

import (
	"context"
	"io"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	dstdcopy "github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"
	"golang.org/x/term"
)

// attachAndWait wires the host terminal to the already-started Run container and
// blocks until it exits. This reproduces `docker run -it` so that the agent the
// in-sandbox manager launches (whose stdio is the container's stdio) is wired to
// the user's terminal. The control websocket is unaffected; it carries only
// control-plane RPCs.
func (s *instance) attachAndWait(ctx context.Context, ctr testcontainers.Container) (int, error) {
	cli, err := s.containers.Client(ctx)
	if err != nil {
		return 1, err
	}
	id := ctr.GetContainerID()
	tty := stdinIsTerminal() && stdoutIsTerminal()

	attach, err := cli.ContainerAttach(ctx, id, client.ContainerAttachOptions{
		Stream: true,
		Stdin:  true,
		Stdout: true,
		Stderr: true,
	})
	if err != nil {
		return 1, err
	}
	defer attach.Close()

	if tty {
		if state, err := term.MakeRaw(int(os.Stdin.Fd())); err == nil {
			defer func() { _ = term.Restore(int(os.Stdin.Fd()), state) }()
		}
		resizeContainer(ctx, cli, id)
		winch := make(chan os.Signal, 1)
		signal.Notify(winch, syscall.SIGWINCH)
		defer signal.Stop(winch)
		go func() {
			for range winch {
				resizeContainer(ctx, cli, id)
			}
		}()
	}

	// In raw TTY mode Ctrl-C is delivered to the container as a byte through the
	// PTY, so we do not forward SIGINT. Without a TTY we forward it like
	// ProcessRunner does.
	sigs := make(chan os.Signal, 4)
	if tty {
		signal.Notify(sigs, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	} else {
		signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	}
	defer signal.Stop(sigs)
	go func() {
		for sig := range sigs {
			if sysSig, ok := sig.(syscall.Signal); ok {
				_, _ = cli.ContainerKill(ctx, id, client.ContainerKillOptions{Signal: signalName(sysSig)})
			}
		}
	}()

	outDone := make(chan struct{})
	go func() {
		defer close(outDone)
		if tty {
			_, _ = io.Copy(os.Stdout, attach.Reader)
		} else {
			_, _ = dstdcopy.StdCopy(os.Stdout, os.Stderr, attach.Reader)
		}
	}()
	go func() {
		_, _ = io.Copy(attach.Conn, os.Stdin)
		_ = attach.CloseWrite()
	}()

	code, waitErr := s.waitExit(ctx, ctr)
	<-outDone
	return code, waitErr
}

func resizeContainer(ctx context.Context, cli *testcontainers.DockerClient, id string) {
	width, height, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		return
	}
	_, _ = cli.ContainerResize(ctx, id, client.ContainerResizeOptions{Height: uint(height), Width: uint(width)})
}

func signalName(sig syscall.Signal) string {
	switch sig {
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGHUP:
		return "SIGHUP"
	case syscall.SIGQUIT:
		return "SIGQUIT"
	case syscall.SIGWINCH:
		return "SIGWINCH"
	default:
		return strconv.Itoa(int(sig))
	}
}
