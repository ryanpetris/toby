package httpbridge

// Verifies configured HTTP headers cannot escape their selected origin.

import (
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

func TestBridgeDoesNotForwardConfiguredHeadersAcrossOrigins(t *testing.T) {
	const secret = "Bearer must-not-leak"

	var targetRequests atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		targetRequests.Add(1)
		if got := request.Header.Get("Authorization"); got != "" {
			t.Errorf("cross-origin Authorization = %q, want empty", got)
		}
		writer.WriteHeader(http.StatusAccepted)
	}))
	defer target.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != secret {
			t.Errorf("origin Authorization = %q, want configured secret", request.Header.Get("Authorization"))
		}
		http.Redirect(writer, request, target.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	bridge, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	host, sandbox := net.Pipe()
	peer := newProtocolPeer(sandbox)
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- bridge.Serve(t.Context(), host, Upstream{
			Endpoint: origin.URL,
			Headers:  http.Header{"Authorization": {secret}},
		})
	}()

	peer.write(t, call(t, "init-redirect", methodInitialize, map[string]any{
		"protocolVersion": testProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "redirect", "version": "1"},
	}))

	err = <-serveDone
	if err == nil {
		t.Fatal("Serve succeeded across an origin-changing redirect")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Serve error leaked configured header value: %v", err)
	}
	if got := targetRequests.Load(); got != 0 {
		t.Fatalf("cross-origin requests = %d, want 0", got)
	}

	_ = peer.Close()
}

func TestBridgeRejectsReservedConfiguredHeaders(t *testing.T) {
	bridge, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	host, sandbox := net.Pipe()
	defer host.Close()
	defer sandbox.Close()

	err = bridge.Serve(t.Context(), host, Upstream{
		Endpoint: "http://127.0.0.1/mcp",
		Headers: http.Header{
			sessionIDHeader: {"attacker-selected"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("Serve error = %v, want reserved-header rejection", err)
	}
}

func TestValidateConfiguredHeadersRejectsBridgeControlledNames(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"Accept",
		"Connection",
		"Content-Length",
		"Content-Type",
		"Host",
		"Keep-Alive",
		"Last-Event-ID",
		"MCP-Protocol-Version",
		"MCP-Session-ID",
		"Proxy-Authorization",
		"Proxy-Connection",
		"TE",
		"Trailer",
		"Transfer-Encoding",
		"Upgrade",
	} {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := ValidateConfiguredHeaders(http.Header{
				name: {"value"},
			})
			if err == nil || !strings.Contains(err.Error(), "reserved") {
				t.Fatalf(
					"ValidateConfiguredHeaders(%q) error = %v, want reserved-header rejection",
					name,
					err,
				)
			}
		})
	}
}

func TestValidateConfiguredHeadersRejectsUnsafeValues(t *testing.T) {
	t.Parallel()

	for _, value := range []string{
		"line\rbreak",
		"line\nbreak",
		"control\x01byte",
		"delete\x7fbyte",
	} {
		err := ValidateConfiguredHeaders(http.Header{
			"Authorization": {value},
		})
		if err == nil || !strings.Contains(err.Error(), "invalid value") {
			t.Fatalf(
				"ValidateConfiguredHeaders(%q) error = %v, want invalid-value rejection",
				value,
				err,
			)
		}
	}
}

func TestBridgeIsolatesCookiesBetweenSessions(t *testing.T) {
	var initializeCount atomic.Int64
	initializedCookies := make(chan string, 2)

	server := httptest.NewServer(http.HandlerFunc(func(
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
		if !ok {
			writer.WriteHeader(http.StatusAccepted)
			return
		}
		if rpcRequest.Method == methodInitialize {
			if cookie := request.Header.Get("Cookie"); cookie != "" {
				t.Errorf("initialize Cookie = %q, want empty", cookie)
			}

			number := initializeCount.Add(1)
			sessionID := fmt.Sprintf("cookie-session-%d", number)
			http.SetCookie(writer, &http.Cookie{
				Name:  "bridge_session",
				Value: sessionID,
				Path:  "/",
			})
			writer.Header().Set(sessionIDHeader, sessionID)
			writeHTTPMessage(t, writer, response(
				t,
				rpcRequest.ID,
				map[string]any{
					"protocolVersion": testProtocolVersion,
					"capabilities":    map[string]any{},
					"serverInfo": map[string]any{
						"name":    "cookies",
						"version": "1",
					},
				},
			))
			return
		}
		if rpcRequest.Method == methodInitialized {
			initializedCookies <- request.Header.Get("Cookie")
		}
		writer.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	sharedJar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New: %v", err)
	}
	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	sharedJar.SetCookies(serverURL, []*http.Cookie{{
		Name:  "shared_secret",
		Value: "must-not-leak",
		Path:  "/",
	}})

	sharedClient := *server.Client()
	sharedClient.Jar = sharedJar
	bridge, err := New(Options{HTTPClient: &sharedClient})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for _, initializeID := range []string{"cookie-one", "cookie-two"} {
		host, sandbox := net.Pipe()
		peer := newProtocolPeer(sandbox)
		done := make(chan error, 1)
		go func() {
			done <- bridge.Serve(
				t.Context(),
				host,
				Upstream{Endpoint: server.URL},
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

	for _, want := range []string{
		"bridge_session=cookie-session-1",
		"bridge_session=cookie-session-2",
	} {
		if got := <-initializedCookies; got != want {
			t.Fatalf("initialized Cookie = %q, want %q", got, want)
		}
	}
	if got := sharedJar.Cookies(serverURL); len(got) != 1 ||
		got[0].Name != "shared_secret" {
		t.Fatalf("shared client jar was mutated: %#v", got)
	}
}

func TestOriginNormalizesDefaultPorts(t *testing.T) {
	tests := []struct {
		endpoint string
		target   string
		want     bool
	}{
		{
			endpoint: "https://example.com/mcp",
			target:   "https://EXAMPLE.com:443/redirect",
			want:     true,
		},
		{
			endpoint: "http://example.com:80/mcp",
			target:   "http://example.com/redirect",
			want:     true,
		},
		{
			endpoint: "https://example.com/mcp",
			target:   "https://example.com:8443/redirect",
			want:     false,
		},
	}

	for _, test := range tests {
		t.Run(test.endpoint+" to "+test.target, func(t *testing.T) {
			endpointOrigin, err := parseEndpoint(test.endpoint)
			if err != nil {
				t.Fatalf("parseEndpoint: %v", err)
			}
			target, err := url.Parse(test.target)
			if err != nil {
				t.Fatalf("parse target: %v", err)
			}
			if got := endpointOrigin.matches(target); got != test.want {
				t.Fatalf("origin match = %t, want %t", got, test.want)
			}
		})
	}
}
