package control

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
)

// roundTrip starts a control server whose handler echoes one newline-framed line
// back, dials it, and returns the dialed conn for the test to drive.
func dialTestServer(t *testing.T, handler ConnHandler) (Endpoint, *Server) {
	t.Helper()
	server, err := ListenEndpoint(context.Background(), WebSocketEndpoint("127.0.0.1:0", "secret"),
		Route{Pattern: "/control", Auth: true, Handler: WebSocketHandler(handler)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return server.Endpoint, server
}

func TestWebSocketRoundTrip(t *testing.T) {
	endpoint, _ := dialTestServer(t, func(_ context.Context, conn net.Conn) {
		defer conn.Close()
		line, err := bufio.NewReader(conn).ReadBytes('\n')
		if err != nil {
			return
		}
		_, _ = conn.Write(line)
	})

	conn, err := DialEndpoint(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("ping\n")); err != nil {
		t.Fatal(err)
	}
	got, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "ping\n" {
		t.Fatalf("echo = %q, want %q", got, "ping\n")
	}
}

// TestWebSocketLargePayload locks in the read-limit fix: a message far larger than
// coder/websocket's 32 KiB default must round-trip (file contents ride the channel
// as JSON-RPC params).
func TestWebSocketLargePayload(t *testing.T) {
	endpoint, _ := dialTestServer(t, func(_ context.Context, conn net.Conn) {
		defer conn.Close()
		line, err := bufio.NewReader(conn).ReadBytes('\n')
		if err != nil {
			return
		}
		_, _ = conn.Write(line)
	})

	conn, err := DialEndpoint(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	payload := strings.Repeat("x", 1<<20) + "\n" // 1 MiB, well past 32 KiB
	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	got, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != payload {
		t.Fatalf("large echo mismatch: got %d bytes, want %d", len(got), len(payload))
	}
}

func TestDialRejectedWithoutToken(t *testing.T) {
	endpoint, _ := dialTestServer(t, func(_ context.Context, conn net.Conn) { _ = conn.Close() })

	bad := endpoint
	bad.Token = ""
	conn, err := DialEndpoint(context.Background(), bad)
	if err == nil {
		_ = conn.Close()
		t.Fatal("expected unauthorized dial to fail")
	}
}
