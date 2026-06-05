package control

import (
	"strings"
	"testing"
)

func TestDecodeRequestValidatesEnvelope(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		wantErr string
	}{
		{name: "valid", data: `{"jsonrpc":"2.0","id":1,"method":"git.fetch"}`},
		{name: "malformed", data: `{`, wantErr: "parse request"},
		{name: "wrong version", data: `{"jsonrpc":"1.0","id":1,"method":"git.fetch"}`, wantErr: "invalid JSON-RPC request"},
		{name: "missing id", data: `{"jsonrpc":"2.0","method":"git.fetch"}`, wantErr: "invalid JSON-RPC request"},
		{name: "empty method", data: `{"jsonrpc":"2.0","id":1}`, wantErr: "invalid JSON-RPC request"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeRequest([]byte(tt.data))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestDefaultEndpointUsesControlHost(t *testing.T) {
	t.Setenv(EnvControlHost, "127.0.0.1:1234")
	t.Setenv(EnvControlToken, "secret")
	endpoint, err := DefaultEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.Host != "127.0.0.1:1234" || endpoint.Token != "secret" {
		t.Fatalf("endpoint = %#v", endpoint)
	}
	if endpoint.ControlURL() != "ws://127.0.0.1:1234/control" {
		t.Fatalf("control URL = %q", endpoint.ControlURL())
	}
	if endpoint.BinaryURL() != "http://127.0.0.1:1234/binary" {
		t.Fatalf("binary URL = %q", endpoint.BinaryURL())
	}
	if endpoint.ProxyBaseURL("local/proxy") != "http://127.0.0.1:1234/proxy/local%2Fproxy" {
		t.Fatalf("proxy URL = %q", endpoint.ProxyBaseURL("local/proxy"))
	}
}

func TestDefaultEndpointValidationAndURLHelpers(t *testing.T) {
	t.Setenv(EnvControlHost, "")
	if _, err := DefaultEndpoint(); err == nil || !strings.Contains(err.Error(), EnvControlHost+" is required") {
		t.Fatalf("missing endpoint err = %v", err)
	}
	t.Setenv(EnvControlHost, "localhost")
	if _, err := DefaultEndpoint(); err == nil || !strings.Contains(err.Error(), "invalid "+EnvControlHost) {
		t.Fatalf("invalid endpoint err = %v", err)
	}
	t.Setenv(EnvControlHost, " [::1]:1234 ")
	t.Setenv(EnvControlToken, "token")
	endpoint, err := DefaultEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.Host != "[::1]:1234" || endpoint.ControlURL() != "ws://[::1]:1234/control" || endpoint.BinaryURL() != "http://[::1]:1234/binary" {
		t.Fatalf("endpoint = %#v", endpoint)
	}
	if endpoint.ProxyBaseURL("space id/slash") != "http://[::1]:1234/proxy/space%20id%2Fslash" {
		t.Fatalf("proxy URL = %q", endpoint.ProxyBaseURL("space id/slash"))
	}
	if err := validateHostPort(":1234"); err == nil {
		t.Fatal("expected empty host to fail")
	}
	if err := validateHostPort("localhost:"); err == nil {
		t.Fatal("expected empty port to fail")
	}
}
