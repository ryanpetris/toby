package mcpproxy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"petris.dev/toby/container/engine"
	appconfig "petris.dev/toby/internal/config/app"
	"petris.dev/toby/internal/control/httpproxy"
	"petris.dev/toby/internal/control/tunnel"
	"petris.dev/toby/internal/daemon/resource"
)

// A remote server needs no container, so Configure is deterministic without Docker: it
// acquires the shared upstream backend and registers it on the project proxy.
func TestConfigureRegistersRemoteProxyURL(t *testing.T) {
	cfg := testConfig(t, []byte(`
mcps:
  servers:
    remote:
      type: remote
      url: https://example.com/mcp
`))
	proxy := httpproxy.NewService(nil)
	registry := resource.NewRegistry(resource.NewDockerRunner(engine.New()))
	svc, err := NewService(ServiceParams{Proxy: proxy, Registry: registry})
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.Configure(context.Background(), cfg, Defaults{}); err != nil {
		t.Fatal(err)
	}
	url, ok := svc.URL("remote")
	if !ok || !strings.HasPrefix(url, "http://"+tunnel.ProxyAddr+"/proxy/") {
		t.Fatalf("url = %q, %v", url, ok)
	}
	status := svc.Status()
	if len(status) != 1 || status[0].Name != "remote" {
		t.Fatalf("status = %#v", status)
	}

	// Releasing the project's leases leaves nothing behind.
	svc.Close()
	if got := svc.Status(); len(got) != 0 {
		t.Fatalf("status after close = %#v", got)
	}
}

func testConfig(t *testing.T, data []byte) *appconfig.Service {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := appconfig.Load(dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
