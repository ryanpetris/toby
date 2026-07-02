package daemon

// Transport parity: the daemon.* round trip must behave identically over the unix
// socket and the WebSocket transport, because control.Peer writes the exact same
// payloads over whatever net.Conn a transport yields.

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"petris.dev/toby/config"
	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/daemon/protocol"
	"petris.dev/toby/internal/daemon/transport"
	"petris.dev/toby/internal/daemon/transport/unixsocket"
	"petris.dev/toby/internal/daemon/transport/websocket"

	"go.uber.org/fx"
)

type fakeShutdowner struct{}

func (fakeShutdowner) Shutdown(...fx.ShutdownOption) error { return nil }

func startTestDaemon(t *testing.T, listener transport.Listener) {
	t.Helper()
	router, err := control.NewRouter([]control.Capability{
		newMethods(fakeShutdowner{}, Options{}, "test-version", time.Now(), nil),
	})
	if err != nil {
		t.Fatalf("router: %v", err)
	}
	service := newService(listener, router, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go service.serve(ctx)
	t.Cleanup(func() {
		cancel()
		_ = service.close()
	})
}

func dialWithRetry(t *testing.T, connector transport.Connector) *control.Peer {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, err := connector.Connect(context.Background())
		if err == nil {
			peer := control.NewPeer(context.Background(), conn, nil)
			peer.Start(nil)
			t.Cleanup(func() { _ = peer.Close() })
			return peer
		}
		if time.Now().After(deadline) {
			t.Fatalf("connect: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestTransportParity(t *testing.T) {
	cases := []struct {
		name string
		make func(t *testing.T) (transport.Listener, transport.Connector)
	}{
		{
			name: "unix",
			make: func(t *testing.T) (transport.Listener, transport.Connector) {
				paths := config.Paths{RuntimeDir: t.TempDir()}
				return unixsocket.New(paths), unixsocket.New(paths)
			},
		},
		{
			name: "websocket",
			make: func(t *testing.T) (transport.Listener, transport.Connector) {
				cfg := websocket.Config{Address: "127.0.0.1:47812"}
				return websocket.New(cfg), websocket.New(cfg)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			listener, connector := tc.make(t)
			startTestDaemon(t, listener)
			peer := dialWithRetry(t, connector)

			// ping
			var ping protocol.PingResult
			mustCall(t, peer, protocol.MethodDaemonPing, protocol.PingParams{Version: "client"}, &ping)
			if ping.Version != "test-version" {
				t.Fatalf("ping version = %q, want test-version", ping.Version)
			}
			if ping.PID != os.Getpid() {
				t.Fatalf("ping pid = %d, want %d", ping.PID, os.Getpid())
			}

			// status (a second message over the same conn exercises multi-message framing)
			var status protocol.StatusResult
			mustCall(t, peer, protocol.MethodDaemonStatus, nil, &status)
			if status.Version != "test-version" {
				t.Fatalf("status version = %q, want test-version", status.Version)
			}
			if len(status.Projects) != 0 {
				t.Fatalf("status projects = %d, want 0", len(status.Projects))
			}
		})
	}
}

func mustCall(t *testing.T, peer *control.Peer, method string, params any, out any) {
	t.Helper()
	resp, err := peer.Call(context.Background(), method, params)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	if resp.Result == nil {
		return
	}
	data, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
}
