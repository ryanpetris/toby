package builtin

// Exercises fresh built-in sessions, snapshot decoding, and run revocation.

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/agent/protocol"
	agentserver "petris.dev/toby/internal/agent/server"
	"petris.dev/toby/internal/mcpgateway"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/tobymcp"
)

func TestResolverServesFreshSessionPerConnector(t *testing.T) {
	t.Parallel()

	runner, err := tobymcp.NewRunner(tobymcp.RunnerParams{})
	if err != nil {
		t.Fatal(err)
	}
	var decodeCalls atomic.Int32
	resolver, err := NewResolver(
		runner,
		SessionDecoder(func(
			data json.RawMessage,
		) (tobymcp.SessionSnapshot, error) {
			decodeCalls.Add(1)
			if string(data) != `{"debug":false}` {
				t.Fatalf("session snapshot = %s", data)
			}
			return validBuiltinTestSnapshot(), nil
		}),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	prepared, err := resolver.Resolve(
		t.Context(),
		mcpgateway.TargetRequest{
			ResourceID: protocol.ResourceID("resource-one"),
			Caller:     builtinTestCaller{},
			Name:       mcpgateway.BuiltinTarget,
			Session:    json.RawMessage(`{"debug":false}`),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeCalls.Load(); got != 1 {
		t.Fatalf("session decode calls = %d, want 1", got)
	}

	target, err := prepared.Acquire(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		serverConn, clientConn := unixConnectionPair(t)
		result := make(chan struct{})
		go func() {
			target.Target().ServeConnector(t.Context(), serverConn)
			close(result)
		}()

		client := mcp.NewClient(
			&mcp.Implementation{Name: "test", Version: "1"},
			nil,
		)
		session, err := client.Connect(
			t.Context(),
			&mcp.IOTransport{Reader: clientConn, Writer: clientConn},
			nil,
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := session.Close(); err != nil &&
			!errors.Is(err, net.ErrClosed) {
			t.Fatal(err)
		}

		select {
		case <-result:
		case <-time.After(5 * time.Second):
			t.Fatal("built-in handler did not return after client close")
		}
	}
	if err := target.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestAcquiredTargetRevokeClosesLiveSession(t *testing.T) {
	t.Parallel()

	runner, err := tobymcp.NewRunner(tobymcp.RunnerParams{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := NewResolver(
		runner,
		SessionDecoder(func(
			json.RawMessage,
		) (tobymcp.SessionSnapshot, error) {
			return validBuiltinTestSnapshot(), nil
		}),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := resolver.Resolve(
		t.Context(),
		mcpgateway.TargetRequest{
			ResourceID: protocol.ResourceID("resource-revoke"),
			Caller:     builtinTestCaller{},
			Name:       mcpgateway.BuiltinTarget,
			Session:    json.RawMessage(`{}`),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	target, err := prepared.Acquire(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}

	serverConn, clientConn := unixConnectionPair(t)
	result := make(chan struct{})
	go func() {
		target.Target().ServeConnector(t.Context(), serverConn)
		close(result)
	}()

	client := mcp.NewClient(
		&mcp.Implementation{Name: "test", Version: "1"},
		nil,
	)
	session, err := client.Connect(
		t.Context(),
		&mcp.IOTransport{Reader: clientConn, Writer: clientConn},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	target.Revoke()

	select {
	case <-result:
	case <-time.After(5 * time.Second):
		t.Fatal("built-in handler remained live after target revocation")
	}
	_ = session.Close()
}

func validBuiltinTestSnapshot() tobymcp.SessionSnapshot {
	return tobymcp.SessionSnapshot{
		Runtime: tobymcp.SessionRuntime{
			Name:      "test",
			Profile:   "default",
			Runtime:   "bubblewrap",
			Home:      layout.Home,
			Workspace: layout.Workspace,
			Root:      layout.Root,
			Bin:       layout.Bin,
			Workdir:   layout.Workspace,
		},
	}
}

type builtinTestCaller struct{}

var _ agentserver.HostActionCaller = builtinTestCaller{}

func (builtinTestCaller) Call(
	context.Context,
	json.RawMessage,
) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}

func unixConnectionPair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()

	descriptors, err := unix.Socketpair(
		unix.AF_UNIX,
		unix.SOCK_STREAM|unix.SOCK_CLOEXEC,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	files := []*os.File{
		os.NewFile(uintptr(descriptors[0]), "server"),
		os.NewFile(uintptr(descriptors[1]), "client"),
	}
	connections := make([]*net.UnixConn, 2)
	for index, file := range files {
		raw, err := net.FileConn(file)
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			t.Fatal(err)
		}
		connection, ok := raw.(*net.UnixConn)
		if !ok {
			t.Fatalf("socketpair connection = %T, want *net.UnixConn", raw)
		}
		connections[index] = connection
	}

	return connections[0], connections[1]
}
