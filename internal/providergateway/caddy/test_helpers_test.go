//go:build linux

package caddy

// Provides an actual Unix-socket HTTP fixture for administration client tests.

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"testing"
)

func newAdminTestServer(
	t *testing.T,
	handler http.Handler,
) Connector {
	t.Helper()

	socket := filepath.Join(t.TempDir(), "admin.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal("listen on test admin socket:", err)
	}

	server := &http.Server{
		Handler:  handler,
		ErrorLog: log.New(io.Discard, "", 0),
	}
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- server.Serve(listener)
	}()

	t.Cleanup(func() {
		closeErr := server.Close()
		serveErr := <-serveDone
		if closeErr != nil {
			t.Errorf("close test admin server: %v", closeErr)
		}
		if serveErr != nil &&
			!errors.Is(serveErr, http.ErrServerClosed) &&
			!errors.Is(serveErr, net.ErrClosed) {
			t.Errorf("serve test admin socket: %v", serveErr)
		}
	})

	return func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}
}

func newAdminTestClient(
	t *testing.T,
	handler http.Handler,
	options Options,
) *Client {
	t.Helper()

	client, err := New(newAdminTestServer(t, handler), options)
	if err != nil {
		t.Fatal("construct test admin client:", err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("close test admin client: %v", err)
		}
	})

	return client
}
