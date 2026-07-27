//go:build !windows

package httpbridge

// Verifies one Unix-socket client override cannot replace the Bridge default.

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

func TestBridgeUsesHTTPClientOverrideForOnlyOneServe(t *testing.T) {
	handler := func(
		name string,
		sessionID string,
		initializeCount *atomic.Int64,
	) http.Handler {
		return http.HandlerFunc(func(
			writer http.ResponseWriter,
			request *http.Request,
		) {
			switch request.Method {
			case http.MethodGet:
				writer.WriteHeader(http.StatusMethodNotAllowed)
				return
			case http.MethodDelete:
				writer.WriteHeader(http.StatusNoContent)
				return
			}

			message := readHTTPRequest(t, request)
			rpcRequest, ok := message.(*jsonrpc.Request)
			if !ok || rpcRequest.Method != methodInitialize {
				writer.WriteHeader(http.StatusAccepted)
				return
			}

			initializeCount.Add(1)
			writer.Header().Set(sessionIDHeader, sessionID)
			writeHTTPMessage(t, writer, response(
				t,
				rpcRequest.ID,
				map[string]any{
					"protocolVersion": testProtocolVersion,
					"capabilities":    map[string]any{},
					"serverInfo": map[string]any{
						"name":    name,
						"version": "1",
					},
				},
			))
		})
	}

	var unixInitializes atomic.Int64
	socketPath := filepath.Join(t.TempDir(), "mcp.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen on Unix socket: %v", err)
	}
	unixServer := &http.Server{
		Handler: handler("unix", "session-unix", &unixInitializes),
	}
	unixDone := make(chan error, 1)
	go func() {
		unixDone <- unixServer.Serve(listener)
	}()
	t.Cleanup(func() {
		if err := unixServer.Close(); err != nil {
			t.Errorf("close Unix HTTP server: %v", err)
		}
		if err := <-unixDone; err != nil &&
			!errors.Is(err, http.ErrServerClosed) {
			t.Errorf("serve Unix HTTP: %v", err)
		}
	})

	unixTransport := &http.Transport{
		Proxy: nil,
		DialContext: func(
			ctx context.Context,
			_, _ string,
		) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(
				ctx,
				"unix",
				socketPath,
			)
		},
	}
	t.Cleanup(unixTransport.CloseIdleConnections)
	unixClient := &http.Client{Transport: unixTransport}

	var remoteInitializes atomic.Int64
	remoteServer := httptest.NewServer(
		handler("remote", "session-remote", &remoteInitializes),
	)
	defer remoteServer.Close()

	bridge, err := New(Options{HTTPClient: remoteServer.Client()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	run := func(
		endpoint string,
		client *http.Client,
		initializeID string,
	) {
		host, sandbox := net.Pipe()
		peer := newProtocolPeer(sandbox)
		done := make(chan error, 1)
		go func() {
			done <- bridge.Serve(
				t.Context(),
				host,
				Upstream{
					Endpoint:   endpoint,
					HTTPClient: client,
				},
			)
		}()

		initialize(t, peer, initializeID)
		if err := peer.Close(); err != nil {
			t.Fatalf("close peer: %v", err)
		}
		if err := <-done; err != nil {
			t.Fatalf("Serve: %v", err)
		}
	}

	run("http://mcp.local/mcp", unixClient, "init-unix")
	run(remoteServer.URL, nil, "init-remote")

	if got := unixInitializes.Load(); got != 1 {
		t.Fatalf("Unix initializes = %d, want 1", got)
	}
	if got := remoteInitializes.Load(); got != 1 {
		t.Fatalf("remote initializes = %d, want 1", got)
	}
}
