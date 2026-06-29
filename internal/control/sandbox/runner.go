// Package sandbox is the in-sandbox manager. It carries a single gRPC connection
// to the host over stdio (stdin/stdout), binds a local HTTP listener, tunnels every
// accepted proxy connection to the host, and executes manager-scoped filesystem
// control requests inside the sandbox.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"

	"petris.dev/toby/internal/control"
	filescap "petris.dev/toby/internal/control/methods/files"
	"petris.dev/toby/internal/control/stdio"
	"petris.dev/toby/internal/control/tunnel"
)

// proxyListenAddr is the fixed loopback address the manager binds; see tunnel.ProxyAddr.
const proxyListenAddr = tunnel.ProxyAddr

// benignConnErr reports whether err is a normal connection teardown (the proxy
// client closing a kept-alive connection), which is not worth logging.
func benignConnErr(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "connection reset by peer") ||
		strings.Contains(msg, "use of closed network connection") ||
		strings.Contains(msg, "broken pipe")
}

// Runner is the fx-provided entry point for the hidden `toby sandbox manager`
// command.
type Runner struct {
	router *control.Router
}

func NewRunner() (*Runner, error) {
	router, err := control.NewRouter([]control.Capability{filescap.New()})
	if err != nil {
		return nil, err
	}
	return &Runner{router: router}, nil
}

// Run dials the host over stdio, binds the local proxy listener, reports readiness,
// and forwards every accepted connection to the host until the context is cancelled
// (the host stops the container) or the listener closes.
func (r *Runner) Run(ctx context.Context) error {
	if r == nil || r.router == nil {
		return fmt.Errorf("sandbox manager is not configured")
	}
	// stdout (fd 1) carries only gRPC frames; route all logging to stderr (fd 2).
	log.SetOutput(os.Stderr)

	link := stdio.NewConn(os.Stdin, os.Stdout, nil)
	cc, client, err := tunnel.Dial(link)
	if err != nil {
		return err
	}
	defer cc.Close()

	lis, err := net.Listen("tcp", proxyListenAddr)
	if err != nil {
		return fmt.Errorf("bind proxy listener %s: %w", proxyListenAddr, err)
	}
	defer lis.Close()

	if _, err := client.Ready(ctx, &tunnel.ReadyRequest{Addr: lis.Addr().String()}); err != nil {
		return fmt.Errorf("ready handshake: %w", err)
	}

	go func() {
		if err := r.runControl(ctx, client); err != nil && ctx.Err() == nil {
			log.Printf("control stream: %v", err)
		}
	}()

	go func() {
		<-ctx.Done()
		_ = lis.Close()
	}()

	for {
		conn, err := lis.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go func() {
			if err := tunnel.Forward(ctx, client, conn); err != nil && ctx.Err() == nil && !benignConnErr(err) {
				log.Printf("tunnel forward: %v", err)
			}
		}()
	}
}

func (r *Runner) runControl(ctx context.Context, client tunnel.TunnelClient) error {
	stream, err := client.Control(ctx)
	if err != nil {
		return err
	}
	conn := tunnel.NewStreamConn(stream, nil)
	peer := control.NewPeer(ctx, conn, r.handleControl)
	peer.Start(nil)
	select {
	case <-peer.Done():
		return peer.Err()
	case <-ctx.Done():
		_ = peer.Close()
		return ctx.Err()
	}
}

func (r *Runner) handleControl(ctx context.Context, data []byte) ([]byte, error) {
	req, err := control.DecodeRequest(data)
	if err != nil {
		return control.ResponseError(nil, control.CodeInvalidRequest, err.Error(), nil), err
	}
	return r.router.Handle(ctx, req)
}
