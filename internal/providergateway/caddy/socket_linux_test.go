//go:build linux

package caddy

// Proves descriptor-relative Caddy socket verification and dialing beyond the
// Linux Unix-address path-length limit.

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestDialSocketAvoidsLongHostPath(t *testing.T) {
	directory := t.TempDir()
	const component = "long-caddy-runtime-component"
	for len(filepath.Join(directory, "data.sock")) <
		len(unix.RawSockaddrUnix{}.Path)+32 {
		directory = filepath.Join(directory, component)
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}

	runtime, err := os.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	const name = "data.sock"
	address := filepath.Join(
		"/proc/self/fd",
		strconv.FormatUint(uint64(runtime.Fd()), 10),
		name,
	)
	listener, err := net.ListenUnix(
		"unix",
		&net.UnixAddr{Name: address, Net: "unix"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	hostPath := filepath.Join(directory, name)
	if len(hostPath) <= len(unix.RawSockaddrUnix{}.Path) {
		t.Fatalf(
			"host socket path length = %d, want over %d",
			len(hostPath),
			len(unix.RawSockaddrUnix{}.Path),
		)
	}
	if err := os.Chmod(hostPath, 0o600); err != nil {
		t.Fatal(err)
	}

	accepted := make(chan error, 1)
	go func() {
		connection, err := listener.AcceptUnix()
		if err == nil {
			err = connection.Close()
		}
		accepted <- err
	}()

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	connection, err := dialSocket(
		ctx,
		runtime,
		name,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-accepted:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("descriptor-relative socket connection was not accepted")
	}
}
