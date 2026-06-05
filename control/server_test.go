package control

import (
	"context"
	"io"
	"net/http"
	"testing"
)

// startServer starts a control server with the given routes and returns its base
// URL. These tests exercise the HTTP routes only.
func startServer(t *testing.T, token string, routes ...Route) string {
	t.Helper()
	server, err := ListenEndpoint(context.Background(), WebSocketEndpoint("127.0.0.1:0", token), routes...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return "http://" + server.Endpoint.Host
}

func get(t *testing.T, url, token string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func okHandler(marker string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, marker)
	})
}

func TestServerPublicRouteNeedsNoToken(t *testing.T) {
	base := startServer(t, "secret", Route{Pattern: "/pub", Handler: okHandler("public")})

	code, body := get(t, base+"/pub", "")
	if code != http.StatusOK || body != "public" {
		t.Fatalf("public route = %d %q, want 200 public", code, body)
	}
}

func TestServerAuthRouteRequiresToken(t *testing.T) {
	base := startServer(t, "secret", Route{Pattern: "/sec", Handler: okHandler("secret-ok"), Auth: true})

	if code, _ := get(t, base+"/sec", ""); code != http.StatusUnauthorized {
		t.Fatalf("no token = %d, want 401", code)
	}
	if code, _ := get(t, base+"/sec", "wrong"); code != http.StatusUnauthorized {
		t.Fatalf("wrong token = %d, want 401", code)
	}
	code, body := get(t, base+"/sec", "secret")
	if code != http.StatusOK || body != "secret-ok" {
		t.Fatalf("valid token = %d %q, want 200 secret-ok", code, body)
	}
}

func TestServerSubtreeRouteMatchesSuffix(t *testing.T) {
	base := startServer(t, "secret", Route{Pattern: "/proxy/*", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.URL.Path)
	})})

	code, body := get(t, base+"/proxy/abc/v1/models", "")
	if code != http.StatusOK || body != "/proxy/abc/v1/models" {
		t.Fatalf("subtree route = %d %q", code, body)
	}
}
