// Package sandbox is the in-sandbox manager: a proxy-only process. It carries a
// single gRPC connection to the host over its stdio (stdin/stdout), binds a local
// HTTP listener, and tunnels every accepted connection to the host, which owns the
// upstream credentials and dials the real destinations. It runs no commands and
// serves no control RPCs — the host drives exec/file/mount operations directly via
// docker exec/cp.
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

	"petris.dev/toby/control/stdio"
	"petris.dev/toby/control/tunnel"
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
type Runner struct{}

func NewRunner() *Runner { return &Runner{} }

// Run dials the host over stdio, binds the local proxy listener, reports readiness,
// and forwards every accepted connection to the host until the context is cancelled
// (the host stops the container) or the listener closes.
func (r *Runner) Run(ctx context.Context) error {
	if r == nil {
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
