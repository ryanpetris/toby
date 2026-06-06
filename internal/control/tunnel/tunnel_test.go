package tunnel

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"petris.dev/toby/internal/control/stdio"

	"google.golang.org/grpc"
)

type fakeProxy struct{ body string }

func (f fakeProxy) HandleHTTP(_ context.Context, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Echo-Path", r.URL.Path)
	_, _ = io.WriteString(w, f.body)
}

// TestTunnelProxiesHTTPOverStdio drives the whole transport in-process: a gRPC
// server (host) and client (sandbox) connected by a net.Pipe standing in for the
// container stdio link, an HTTP request forwarded over a Connect stream, and the
// host reverse proxy's response streamed back.
func TestTunnelProxiesHTTPOverStdio(t *testing.T) {
	hostConn, sandboxConn := net.Pipe()

	readyCh := make(chan string, 1)
	srv := NewServer(fakeProxy{body: "hello-tunnel"}, func(addr string) {
		select {
		case readyCh <- addr:
		default:
		}
	})
	defer srv.Close()
	gs := grpc.NewServer()
	RegisterTunnelServer(gs, srv)
	go func() { _ = gs.Serve(stdio.NewListener(hostConn)) }()
	defer gs.Stop()

	cc, client, err := Dial(sandboxConn)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer cc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := client.Ready(ctx, &ReadyRequest{Addr: "127.77.0.1:47600"}); err != nil {
		t.Fatalf("ready: %v", err)
	}
	select {
	case addr := <-readyCh:
		if addr != "127.77.0.1:47600" {
			t.Fatalf("ready addr = %q", addr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("onReady was not fired")
	}

	// One accepted proxy connection: clientSide is the in-sandbox HTTP client,
	// managerSide is what the manager accepted and forwards over the tunnel.
	clientSide, managerSide := net.Pipe()
	go func() { _ = Forward(ctx, client, managerSide) }()

	req, err := http.NewRequest(http.MethodGet, "http://proxy/proxy/abc/foo", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Connection", "close")
	writeErr := make(chan error, 1)
	go func() { writeErr <- req.Write(clientSide) }()

	resp, err := http.ReadResponse(bufio.NewReader(clientSide), req)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != "hello-tunnel" {
		t.Fatalf("body = %q", body)
	}
	if got := resp.Header.Get("X-Echo-Path"); got != "/proxy/abc/foo" {
		t.Fatalf("proxied path = %q", got)
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("write request: %v", err)
	}
}
