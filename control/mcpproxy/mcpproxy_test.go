package mcpproxy

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"petris.dev/toby/config/app"
	"petris.dev/toby/container/engine"
	"petris.dev/toby/control/httpproxy"

	dcontainer "github.com/moby/moby/api/types/container"
)

func TestStdioRequestKeepsStdinOpenWithoutTTY(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	runner := NewDockerRunner(engine.New())
	req := runner.stdioRequest(SidecarSpec{
		Name:      "docs",
		Transport: TransportStdio,
		Command:   []string{"docs-mcp", "--stdio"},
		Env:       map[string]string{"TOKEN": "abc"},
		Image:     "docs:latest",
	})

	if req.Image != "docs:latest" {
		t.Fatalf("image = %q", req.Image)
	}
	if !slices.Equal(req.Cmd, []string{"docs-mcp", "--stdio"}) {
		t.Fatalf("cmd = %#v", req.Cmd)
	}
	if req.Env["TOKEN"] != "abc" || req.Env["TERM"] != "xterm-256color" {
		t.Fatalf("env = %#v", req.Env)
	}
	if req.Started {
		t.Fatal("stdio request must be created unstarted so we can attach before output")
	}
	c := &dcontainer.Config{}
	req.ConfigModifier(c)
	if !c.OpenStdin || c.Tty {
		t.Fatalf("stdio config must keep stdin open without a TTY: %#v", c)
	}
}

func TestHTTPRequestExposesContainerPort(t *testing.T) {
	runner := NewDockerRunner(engine.New())
	req := runner.httpRequest(SidecarSpec{
		Name:      "docs",
		Transport: TransportHTTP,
		Command:   []string{"docs-mcp"},
		Image:     "docs:latest",
		HTTPPort:  3000,
	})

	if !slices.Equal(req.ExposedPorts, []string{"3000/tcp"}) {
		t.Fatalf("exposed ports = %#v", req.ExposedPorts)
	}
	if !req.Started {
		t.Fatal("http request must start so the mapped port can be read")
	}
}

func TestConfigureRegistersRemoteAndLocalProxyURLs(t *testing.T) {
	cfg := testConfig(t, []byte(`
mcp:
  server:
    docs:
      type: local
      command: docs-mcp
    remote:
      type: remote
      url: https://example.com/mcp
`))
	proxy := httpproxy.NewService(nil)
	svc, err := NewService(ServiceParams{Proxy: proxy, Runner: NewDockerRunner(engine.New())})
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.Configure(context.Background(), "127.0.0.1:12345", cfg, Defaults{}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"docs", "remote"} {
		url, ok := svc.URL(name)
		if !ok || !strings.HasPrefix(url, "http://127.0.0.1:12345/proxy/") {
			t.Fatalf("url %s = %q, %v", name, url, ok)
		}
	}
	status := svc.Status()
	if len(status) != 2 || status[0].Name != "docs" || status[0].Status != StatusRegistered || status[0].Transport != TransportStdio {
		t.Fatalf("status = %#v", status)
	}
}

func TestSidecarSpecImagePrecedence(t *testing.T) {
	cfg := testConfig(t, []byte(`
mcp:
  server:
    docs:
      type: local
      image: docs:latest
      command: docs-mcp
    fallback:
      type: local
      command: fallback-mcp
`))
	servers := cfg.MCPServers()
	defaults := Defaults{Image: "main:latest"}

	docs, err := sidecarSpec("docs", servers["docs"], defaults)
	if err != nil {
		t.Fatal(err)
	}
	if docs.Image != "docs:latest" {
		t.Fatalf("docs image = %q", docs.Image)
	}
	fallback, err := sidecarSpec("fallback", servers["fallback"], defaults)
	if err != nil {
		t.Fatal(err)
	}
	if fallback.Image != "main:latest" {
		t.Fatalf("fallback image = %q", fallback.Image)
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
