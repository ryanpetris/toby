// Package sandbox holds the in-container manager roles. The netns manager (this file)
// runs in the per-project+profile network container: it dials the daemon over stdio,
// binds the loopback proxy listener in the container's network namespace, and forwards
// every accepted connection to the daemon's reverse proxy. Tool containers share this
// container's network namespace, so they reach the proxy at the same address. It does
// no filesystem work — that is the home manager's job.
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

	"petris.dev/toby/internal/control/stdio"
	"petris.dev/toby/internal/control/tunnel"
)

// proxyListenAddr is the fixed loopback address the manager binds; see tunnel.ProxyAddr.
const proxyListenAddr = tunnel.ProxyAddr

// NetnsRunner runs the proxy-only manager.
type NetnsRunner struct{}

func NewNetnsRunner() *NetnsRunner { return &NetnsRunner{} }

// Run dials the daemon over stdio, binds the local proxy listener, reports readiness,
// and forwards every accepted connection until the context is cancelled.
func (r *NetnsRunner) Run(ctx context.Context) error {
	log.SetOutput(os.Stderr) // fd 1 carries gRPC frames

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

// benignConnErr reports whether err is a normal connection teardown, not worth logging.
func benignConnErr(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "connection reset by peer") ||
		strings.Contains(msg, "use of closed network connection") ||
		strings.Contains(msg, "broken pipe")
}
