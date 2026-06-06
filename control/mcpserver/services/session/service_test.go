package sessionservice

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"petris.dev/toby/config"
	appconfig "petris.dev/toby/config/app"
	"petris.dev/toby/container/engine"
	"petris.dev/toby/control/httpproxy"
	"petris.dev/toby/control/mcpproxy"
	"petris.dev/toby/control/mcpserver"
)

func TestDynamicRuntimeResourceIncludesVersion(t *testing.T) {
	session := &mcpserver.Session{State: mcpserver.SessionState{Debug: false}}
	text, err := handler{session}.runtimeResource(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, `"version"`) {
		t.Fatalf("runtime resource missing version: %s", text)
	}
}

func TestResourcesReadReturnsRequestedAndReportsUnknown(t *testing.T) {
	session := &mcpserver.Session{State: mcpserver.SessionState{Debug: false}, Resources: []mcpserver.Resource{
		{URI: "toby://docs/mcps", Name: "toby.docs.mcps", Title: "Toby-Managed MCPs", FS: resourceDocs, FilePath: "resources/mcps.md"},
		{URI: "toby://session/runtime", Name: "toby.session.runtime", Title: "Toby Session Runtime", Text: func(ctx context.Context, session *mcpserver.Session) (string, error) {
			return handler{session}.runtimeResource(ctx)
		}},
	}}

	result, out, err := handler{session}.resourcesRead(context.Background(), nil, ResourcesReadInput{URIs: []string{"toby://session/runtime", "toby://does/not/exist"}})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.IsError {
		t.Fatalf("an unknown uri should mark the result as an error: %#v", result)
	}
	if len(out.Resources) != 2 {
		t.Fatalf("resources = %#v", out.Resources)
	}
	if out.Resources[0].URI != "toby://session/runtime" || !strings.Contains(out.Resources[0].Text, `"version"`) {
		t.Fatalf("runtime read = %#v", out.Resources[0])
	}
	if out.Resources[1].Error == "" {
		t.Fatalf("unknown uri should report an error: %#v", out.Resources[1])
	}

	_, all, err := handler{session}.resourcesRead(context.Background(), nil, ResourcesReadInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Resources) != 2 {
		t.Fatalf("read-all resources = %#v", all.Resources)
	}
	for _, r := range all.Resources {
		if r.Text == "" || r.Error != "" {
			t.Fatalf("read-all entry = %#v", r)
		}
	}
}

func TestIntrospectionResourcesRedactProviderAndMCPSecrets(t *testing.T) {
	cfg := testTobyConfig(t, []byte(`
provider:
  local:
    type: openai
    baseURL: https://secret-provider.example/v1
    headers:
      Authorization: Bearer secret-provider-token
mcp:
  server:
    docs:
      type: local
      command: [secret-mcp-command, --token, secret-command-token]
    remote:
      type: remote
      url: https://secret-mcp.example/mcp
      headers:
        Authorization: Bearer secret-mcp-token
`))
	proxy := httpproxy.NewService(nil)
	mcpProxy, err := mcpproxy.NewService(mcpproxy.ServiceParams{Proxy: proxy, Runner: mcpproxy.NewDockerRunner(engine.New())})
	if err != nil {
		t.Fatal(err)
	}
	if err := mcpProxy.Configure(context.Background(), cfg, mcpproxy.Defaults{}); err != nil {
		t.Fatal(err)
	}
	session := &mcpserver.Session{State: mcpserver.SessionState{Debug: true, Paths: config.Paths{Home: "/secret-home", XDGConfigHome: "/secret-config", ProjectRoot: "/secret-projects", SandboxRoot: "/secret-sandboxes"}, Config: cfg, MCPProxy: mcpProxy}}
	h := handler{session}

	toolsText, err := h.toolsResource(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	mcpsText, err := h.mcpsResource(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	runtimeText, err := h.runtimeResource(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	text := toolsText + mcpsText + runtimeText
	for _, secret := range []string{"secret-provider.example", "secret-provider-token", "secret-mcp.example", "secret-mcp-token", "secret-mcp-command", "secret-command-token"} {
		if strings.Contains(text, secret) {
			t.Fatalf("environment output leaked %q: %s", secret, text)
		}
	}
	if !strings.Contains(text, "/secret-home") {
		t.Fatalf("debug environment should include host paths: %s", text)
	}
}

func TestEnvironmentHidesHostPathsWhenDebugDisabled(t *testing.T) {
	session := &mcpserver.Session{State: mcpserver.SessionState{Debug: false, Paths: config.Paths{Home: "/secret-home"}}}

	text, err := handler{session}.runtimeResource(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "/secret-home") {
		t.Fatalf("runtime resource leaked host path: %s", text)
	}
}

func TestRuntimeInfoSanitizerRemovesUnsafeKeys(t *testing.T) {
	info := sanitizeRuntimeInfo(map[string]any{
		"image":       "node:dev",
		"command":     []any{"secret"},
		"nested":      map[string]any{"container": "kept", "headers": map[string]any{"Authorization": "secret"}},
		"typedSlice":  []map[string]any{{"path": "/tmp/mcp", "headers": "secret"}},
		"environment": map[string]string{"TOKEN": "secret"},
	})
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "node:dev") || !strings.Contains(text, "container") || !strings.Contains(text, "/tmp/mcp") {
		t.Fatalf("runtimeInfo lost safe values: %s", text)
	}
	for _, secret := range []string{"command", "secret", "headers", "Authorization", "environment", "TOKEN"} {
		if strings.Contains(text, secret) {
			t.Fatalf("runtimeInfo leaked unsafe value %q: %s", secret, text)
		}
	}
}

func testTobyConfig(t *testing.T, data []byte) *appconfig.Service {
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
