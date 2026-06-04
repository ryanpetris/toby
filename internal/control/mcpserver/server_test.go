package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"petris.dev/toby/container/manager"
	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/config/toby"
	"petris.dev/toby/internal/control/httpproxy"
	"petris.dev/toby/internal/control/mcpproxy"
)

type testService struct {
	tools     []Tool
	resources []Resource
}

func (s testService) Tools() []Tool { return s.tools }

func (s testService) Resources() []Resource { return s.resources }

func TestNewRunnerRejectsDuplicateTools(t *testing.T) {
	_, err := NewRunner(RunnerParams{Services: []Service{
		testService{tools: []Tool{{Name: "test.tool", Register: noopRegister}}},
		testService{tools: []Tool{{Name: "test.tool", Register: noopRegister}}},
	}})
	if err == nil {
		t.Fatal("expected duplicate tool to fail")
	}
}

func TestNewRunnerRejectsDuplicateResources(t *testing.T) {
	_, err := NewRunner(RunnerParams{Services: []Service{
		testService{resources: []Resource{{URI: "toby://test", Name: "test", Text: staticResourceText("one")}}},
		testService{resources: []Resource{{URI: "toby://test", Name: "test-again", Text: staticResourceText("two")}}},
	}})
	if err == nil {
		t.Fatal("expected duplicate resource to fail")
	}
}

func TestNewRunnerValidatesToolDefinitions(t *testing.T) {
	tests := []struct {
		name string
		tool Tool
	}{
		{name: "empty name", tool: Tool{Register: noopRegister}},
		{name: "nil register", tool: Tool{Name: "test.tool"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewRunner(RunnerParams{Services: []Service{testService{tools: []Tool{tt.tool}}}}); err == nil {
				t.Fatal("expected invalid tool to fail")
			}
		})
	}
}

func TestNewRunnerValidatesResourceDefinitions(t *testing.T) {
	tests := []struct {
		name     string
		resource Resource
	}{
		{name: "empty uri", resource: Resource{Name: "test", Text: staticResourceText("text")}},
		{name: "empty name", resource: Resource{URI: "toby://test", Text: staticResourceText("text")}},
		{name: "missing source", resource: Resource{URI: "toby://test", Name: "test"}},
		{name: "partial static", resource: Resource{URI: "toby://test", Name: "test", FilePath: "test.md"}},
		{name: "multiple sources", resource: Resource{URI: "toby://test", Name: "test", FS: resourceDocs, FilePath: "resources/git.md", Text: staticResourceText("text")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewRunner(RunnerParams{Services: []Service{testService{resources: []Resource{tt.resource}}}}); err == nil {
				t.Fatal("expected invalid resource to fail")
			}
		})
	}
}

func TestNewRunnerSkipsNilServices(t *testing.T) {
	runner, err := NewRunner(RunnerParams{Services: []Service{nil, testService{tools: []Tool{{Name: "test.tool", Register: noopRegister}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.tools) != 1 || runner.tools[0].Name != "test.tool" {
		t.Fatalf("tools = %#v", runner.tools)
	}
}

func TestStaticResourceReadsEmbeddedFile(t *testing.T) {
	resource := Resource{URI: "toby://docs/git", Name: "toby.docs.git", FS: resourceDocs, FilePath: "resources/git.md"}
	text, err := resource.Read(context.Background(), &Server{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "# Toby Git") || strings.Contains(text, "foo/../bar") {
		t.Fatalf("git resource text = %q", text)
	}
}

func TestDynamicRuntimeResourceIncludesVersion(t *testing.T) {
	server := &Server{state: SessionState{Debug: false}}
	text, err := server.runtimeResource(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, `"version"`) {
		t.Fatalf("runtime resource missing version: %s", text)
	}
}

func TestResourcesReadReturnsRequestedAndReportsUnknown(t *testing.T) {
	server := &Server{state: SessionState{Debug: false}, resources: []Resource{
		{URI: "toby://docs/git", Name: "toby.docs.git", Title: "Toby Git", FS: resourceDocs, FilePath: "resources/git.md"},
		{URI: "toby://session/runtime", Name: "toby.session.runtime", Title: "Toby Session Runtime", Text: func(ctx context.Context, toby *Server) (string, error) { return toby.runtimeResource(ctx) }},
	}}

	result, out, err := server.resourcesRead(context.Background(), nil, ResourcesReadInput{URIs: []string{"toby://session/runtime", "toby://does/not/exist"}})
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

	_, all, err := server.resourcesRead(context.Background(), nil, ResourcesReadInput{})
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

func TestGitToolResultMarksNonzeroExitAsError(t *testing.T) {
	if result := gitToolResult(GitOutput{}); result != nil {
		t.Fatalf("zero exit result = %#v", result)
	}
	result := gitToolResult(GitOutput{ExitCode: 1})
	if result == nil || !result.IsError {
		t.Fatalf("nonzero exit result = %#v", result)
	}
}

func TestIntrospectionResourcesRedactProviderAndMCPSecrets(t *testing.T) {
	cfg := testTobyConfig(t, []byte(`
providers:
  local:
    type: openai
    baseURL: https://secret-provider.example/v1
    headers:
      Authorization: Bearer secret-provider-token
mcps:
  docs:
    type: local
    command: [secret-mcp-command, --token, secret-command-token]
  remote:
    type: remote
    url: https://secret-mcp.example/mcp
    headers:
      Authorization: Bearer secret-mcp-token
`))
	proxy := httpproxy.NewService(httpproxy.ServiceParams{})
	mcpProxy, err := mcpproxy.NewService(mcpproxy.ServiceParams{Proxy: proxy, Runtimes: []mcpproxy.Runtime{mcpproxy.NewDockerRunner(manager.New())}})
	if err != nil {
		t.Fatal(err)
	}
	if err := mcpProxy.Configure(context.Background(), "127.0.0.1:12345", cfg, mcpproxy.Defaults{}); err != nil {
		t.Fatal(err)
	}
	server := &Server{state: SessionState{Debug: true, Paths: config.Paths{Home: "/secret-home", XDGConfigHome: "/secret-config", ProjectRoot: "/secret-projects", SandboxRoot: "/secret-sandboxes"}, Config: cfg, MCPProxy: mcpProxy}}

	toolsText, err := server.toolsResource(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	mcpsText, err := server.mcpsResource(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	runtimeText, err := server.runtimeResource(context.Background())
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
	server := &Server{state: SessionState{Debug: false, Paths: config.Paths{Home: "/secret-home"}}}

	text, err := server.runtimeResource(context.Background())
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

func noopRegister(*mcp.Server, *Server) {}

func staticResourceText(text string) func(context.Context, *Server) (string, error) {
	return func(context.Context, *Server) (string, error) { return text, nil }
}

func testTobyConfig(t *testing.T, data []byte) *tobyconfig.Service {
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
