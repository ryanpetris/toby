//go:build linux

package socket

// Provides secure runtime paths and endpoint helpers for Linux socket tests.

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func testSocketPath(t *testing.T) string {
	t.Helper()

	runtimeDirectory := filepath.Join(t.TempDir(), "runtime")
	if err := os.Mkdir(runtimeDirectory, 0o700); err != nil {
		t.Fatalf("create runtime directory: %v", err)
	}

	return filepath.Join(runtimeDirectory, "toby", "agent.sock")
}

func mustElectListener(t *testing.T, path string) *Listener {
	t.Helper()

	election, err := Elect(t.Context(), path, Options{})
	if err != nil {
		t.Fatalf("elect listener: %v", err)
	}
	if election.Listener == nil || election.Conn != nil {
		if election.Conn != nil {
			election.Conn.Close()
		}
		t.Fatalf("election = %#v, want listener only", election)
	}

	t.Cleanup(func() {
		if err := election.Listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close listener: %v", err)
		}
	})

	return election.Listener
}

func acceptOne(t *testing.T, listener *Listener) <-chan acceptResult {
	t.Helper()

	results := make(chan acceptResult, 1)
	go func() {
		accepted, err := listener.Accept()
		conn, ok := accepted.(*net.UnixConn)
		if err == nil && !ok {
			err = fmt.Errorf("accepted connection has type %T", accepted)
		}
		results <- acceptResult{conn: conn, err: err}
	}()

	return results
}

type acceptResult struct {
	conn *net.UnixConn
	err  error
}

func dialAndAccept(
	t *testing.T,
	path string,
	listener *Listener,
) (*net.UnixConn, *net.UnixConn) {
	t.Helper()

	accepted := acceptOne(t, listener)
	client, err := Dial(t.Context(), path, Options{})
	if err != nil {
		t.Fatalf("dial listener: %v", err)
	}

	result := <-accepted
	if result.err != nil {
		client.Close()
		t.Fatalf("accept connection: %v", result.err)
	}

	return client, result.conn
}
