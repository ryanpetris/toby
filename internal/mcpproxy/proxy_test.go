package mcpproxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"petris.dev/toby/internal/tobyconfig"
)

func TestStreamableProxyForwardsMessagesAndHostHeaders(t *testing.T) {
	t.Setenv("PROXY_ENV_TOKEN", "env-secret")
	requests := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer host-secret" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("X-Env") != "env-secret" {
			t.Errorf("X-Env = %q", r.Header.Get("X-Env"))
		}
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusMethodNotAllowed)
		case http.MethodPost:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			requests <- string(body)
			w.Header().Set("Content-Type", "application/json")
			if strings.Contains(string(body), `"method":"initialize"`) {
				w.Header().Set(headerSessionID, "session-1")
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18","serverInfo":{"name":"test","version":"1"},"capabilities":{}}}`))
				return
			}
			if r.Header.Get(headerSessionID) != "session-1" {
				t.Errorf("%s = %q", headerSessionID, r.Header.Get(headerSessionID))
			}
			if r.Header.Get(headerProtocolVersion) != "2025-06-18" {
				t.Errorf("%s = %q", headerProtocolVersion, r.Header.Get(headerProtocolVersion))
			}
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[]}}`))
		default:
			http.Error(w, "bad method", http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)

	svc := &Service{config: testConfig(t, []byte(fmt.Sprintf(`
mcp:
  docs:
    type: remote
    url: %s
    headers:
      Authorization: Bearer host-secret
    env_http_headers:
      X-Env: PROXY_ENV_TOKEN
`, server.URL)))}

	client, host := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = svc.Proxy(ctx, "docs", host, bufio.NewReader(host))
	}()
	if err := client.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(client)

	_, _ = fmt.Fprintln(client, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, `"protocolVersion":"2025-06-18"`) {
		t.Fatalf("initialize response = %q", line)
	}

	_, _ = fmt.Fprintln(client, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	line, err = reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, `"tools":[]`) {
		t.Fatalf("tools response = %q", line)
	}

	for i := 0; i < 2; i++ {
		select {
		case <-requests:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for proxied request")
		}
	}
}

func TestValidateRejectsNonHTTPMCP(t *testing.T) {
	svc := &Service{config: testConfig(t, []byte(`
mcp:
  local:
    type: local
    command: local-mcp
`))}
	if err := svc.Validate("local"); err == nil || !strings.Contains(err.Error(), "not an HTTP MCP server") {
		t.Fatalf("err = %v", err)
	}
}

func testConfig(t *testing.T, data []byte) *tobyconfig.Service {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := tobyconfig.Load(dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
