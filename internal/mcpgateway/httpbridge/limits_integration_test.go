package httpbridge

// Verifies oversized downstream JSON and upstream JSON/SSE terminate sessions.

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

func TestBridgeRejectsOversizedDownstreamMessage(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		requests.Add(1)
		writer.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	bridge, err := New(Options{MaxMessageBytes: 128})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	host, sandbox := net.Pipe()
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- bridge.Serve(
			t.Context(),
			host,
			Upstream{Endpoint: server.URL},
		)
	}()

	if err := sandbox.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set write deadline: %v", err)
	}
	_, _ = fmt.Fprintf(
		sandbox,
		"{\"jsonrpc\":\"2.0\",\"id\":\"large\",\"method\":\"custom/large\",\"params\":{\"value\":\"%s\"}}\n",
		strings.Repeat("x", 512),
	)

	select {
	case err := <-serveDone:
		if !errors.Is(err, ErrMessageTooLarge) {
			t.Fatalf("Serve error = %v, want ErrMessageTooLarge", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not reject oversized downstream message")
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("upstream requests = %d, want 0", got)
	}

	_ = sandbox.Close()
}

func TestBridgeRejectsOversizedUpstreamResponse(t *testing.T) {
	for _, contentType := range []string{
		"application/json",
		"text/event-stream",
	} {
		t.Run(contentType, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(
				writer http.ResponseWriter,
				request *http.Request,
			) {
				if request.Method == http.MethodDelete {
					writer.WriteHeader(http.StatusNoContent)
					return
				}

				message := readHTTPRequest(t, request)
				rpcRequest, ok := message.(*jsonrpc.Request)
				if !ok || rpcRequest.Method != methodInitialize {
					writer.WriteHeader(http.StatusAccepted)
					return
				}

				writer.Header().Set(sessionIDHeader, "session-oversized")
				writer.Header().Set("Content-Type", contentType)
				oversized := response(t, rpcRequest.ID, map[string]any{
					"protocolVersion": testProtocolVersion,
					"capabilities":    map[string]any{},
					"serverInfo":      map[string]any{"name": "oversized", "version": "1"},
					"padding":         strings.Repeat("x", 1024),
				})
				data, err := jsonrpc.EncodeMessage(oversized)
				if err != nil {
					t.Errorf("encode oversized response: %v", err)
					return
				}
				if contentType == "text/event-stream" {
					_, _ = fmt.Fprintf(writer, "data: %s\n\n", data)
					return
				}
				_, _ = writer.Write(data)
			}))
			defer server.Close()

			bridge, err := New(Options{MaxMessageBytes: 512})
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			host, sandbox := net.Pipe()
			peer := newProtocolPeer(sandbox)
			t.Cleanup(func() {
				_ = peer.Close()
			})
			serveDone := make(chan error, 1)
			go func() {
				serveDone <- bridge.Serve(
					t.Context(),
					host,
					Upstream{Endpoint: server.URL},
				)
			}()

			peer.write(t, call(t, "init-oversized", methodInitialize, map[string]any{
				"protocolVersion": testProtocolVersion,
				"capabilities":    map[string]any{},
				"clientInfo":      map[string]any{"name": "oversized", "version": "1"},
			}))

			select {
			case err := <-serveDone:
				if !errors.Is(err, ErrMessageTooLarge) {
					t.Fatalf("Serve error = %v, want ErrMessageTooLarge", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Serve did not reject oversized upstream response")
			}
		})
	}
}
