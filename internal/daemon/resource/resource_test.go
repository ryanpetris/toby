package resource

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"petris.dev/toby/container/engine"
	appconfig "petris.dev/toby/internal/config/app"
	sandboxruntime "petris.dev/toby/sandbox/runtime"

	dcontainer "github.com/moby/moby/api/types/container"
	"gopkg.in/yaml.v3"
)

// mcpServer builds a single configured MCP server named name from a raw config body.
func mcpServer(t *testing.T, name string, server map[string]any) appconfig.MCPServer {
	t.Helper()
	doc := map[string]any{"mcps": map[string]any{"servers": map[string]any{name: server}}}
	data, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := appconfig.Load(dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return cfg.MCPServers()[name]
}

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

func TestSidecarSpecImagePrecedence(t *testing.T) {
	docs := mcpServer(t, "docs", map[string]any{"type": "local", "image": "docs:latest", "command": "docs-mcp"})
	fallback := mcpServer(t, "fallback", map[string]any{"type": "local", "command": "fallback-mcp"})
	defaults := Defaults{Image: "main:latest"}

	spec, err := sidecarSpec("docs", docs, defaults)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Image != "docs:latest" {
		t.Fatalf("docs image = %q", spec.Image)
	}
	spec, err = sidecarSpec("fallback", fallback, defaults)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Image != "main:latest" {
		t.Fatalf("fallback image = %q", spec.Image)
	}
}

func TestResolveDefaultImagePrecedence(t *testing.T) {
	ctx := context.Background()
	if img, err := resolveDefaultImage(ctx, Defaults{Image: "mcp:latest", ContainerImage: "main:latest"}); err != nil || img != "mcp:latest" {
		t.Fatalf("mcp.image precedence = %q, %v", img, err)
	}
	if img, err := resolveDefaultImage(ctx, Defaults{ContainerImage: "main:latest"}); err != nil || img != "main:latest" {
		t.Fatalf("container fallback = %q, %v", img, err)
	}
	if img, err := resolveDefaultImage(ctx, Defaults{}); err != nil || img != sandboxruntime.DefaultImage {
		t.Fatalf("built-in fallback = %q, %v", img, err)
	}
}

func TestLocalKeyStableAcrossIdenticalSpecs(t *testing.T) {
	a := SidecarSpec{Name: "docs", Transport: TransportStdio, Command: []string{"docs-mcp"}, Image: "docs:latest", Env: map[string]string{"A": "1", "B": "2"}}
	b := SidecarSpec{Name: "docs", Transport: TransportStdio, Command: []string{"docs-mcp"}, Image: "docs:latest", Env: map[string]string{"B": "2", "A": "1"}}
	if localKey(a) != localKey(b) {
		t.Fatal("identical specs (env order aside) must share a key")
	}
	c := b
	c.Image = "docs:other"
	if localKey(a) == localKey(c) {
		t.Fatal("a changed image must yield a new key")
	}
}
