package tobyconfig

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"petris.dev/toby/container/layout"
	"petris.dev/toby/container/mount"
	"petris.dev/toby/context/files"
	"petris.dev/toby/diagnostic/warning"
	sandboxapi "petris.dev/toby/sandbox"
	"petris.dev/toby/tools"
)

func TestLoadDeepMergesConfigFiles(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "toby")
	writeFile(t, filepath.Join(dir, "config.json"), []byte(`{
  "mcp": {
    "server": {
      "docs": { "type": "remote", "url": "https://example.com/mcp" }
    }
  },
  "instruction": ["base.md"],
  "provider": {
      "local": { "type": "openai", "headers": { "Authorization": "Bearer base" } }
    },
  "permission": {
    "paths": {
      "~/allowed": "allow",
      "~/allowed/**": "allow"
    }
  },
  "settings": {
    "mountProfile": "default",
    "suppressWarnings": ["*"],
    "autoloadProjectConfig": true,
    "debug": true,
    "yolo": false
  },
  "tool": {
    "opencode": { "mountProfile": "default" }
  },
  "container": { "image": "node:base" }
}`))
	writeFile(t, filepath.Join(dir, "config.yaml"), []byte(`
mcp:
  server:
    docs:
      enabled: true
provider:
  local:
    baseURL: https://models.example.com
permission:
  paths:
    /tmp/shared: allow
settings:
  suppressWarnings:
    - mount.host-backing
  autoloadProjectConfig: false
  debug: false
  yolo: true
tool:
  opencode:
    mountProfile: shared
  claude:
    mountProfile: default
container:
  image: node:custom
  build: {}
`))

	cfg, err := Load(dir, home)
	if err != nil {
		t.Fatal(err)
	}
	// Nested maps deep-merge across source files: docs keeps json's url and gains
	// yaml's enabled.
	mcp := cfg.MCPServers()["docs"].Raw()
	if mcp["url"] != "https://example.com/mcp" || mcp["enabled"] != true {
		t.Fatalf("mcp.docs = %#v", mcp)
	}
	// instruction is set only in json; the yaml file (no instruction key) leaves
	// it intact.
	instructions := cfg.Instructions()
	if len(instructions) != 1 || instructions[0] != "base.md" {
		t.Fatalf("instructions = %#v", instructions)
	}
	provider := cfg.Providers()["local"]
	if provider.Type != ProviderTypeOpenAI || provider.Headers["Authorization"] != "Bearer base" || provider.BaseURL != "https://models.example.com" {
		t.Fatalf("provider = %#v", provider)
	}
	permission := cfg.Permission()
	for pattern, mode := range map[string]string{
		filepath.Join(home, "allowed"):       "allow",
		filepath.Join(home, "allowed", "**"): "allow",
		"/tmp/shared":                        "allow",
	} {
		if permission.Paths[pattern] != mode {
			t.Fatalf("permission paths = %#v", permission.Paths)
		}
	}
	container := cfg.Container()
	if container.Image != "node:custom" {
		t.Fatalf("container = %#v", container)
	}
	if container.Build.Context != dir || container.Build.Dockerfile != filepath.Join(dir, "Dockerfile") {
		t.Fatalf("container build = %#v", container.Build)
	}
	toolProfiles := cfg.ToolMountProfiles()
	if toolProfiles[tools.OpenCodeToolName] != "shared" || toolProfiles[tools.ClaudeToolName] != "default" {
		t.Fatalf("tool mount profiles = %#v", toolProfiles)
	}
	settings := cfg.Settings()
	if settings.MountProfile != "default" {
		t.Fatalf("mount profile = %q", settings.MountProfile)
	}
	// A later source's list replaces an earlier one: yaml's [mount.host-backing]
	// overrides json's ["*"].
	if !settings.SuppressWarnings.Suppresses(warning.MountHostBacking) || settings.SuppressWarnings.Suppresses(warning.OpenCodeModelDiscovery) {
		t.Fatalf("suppress warnings = %#v", settings.SuppressWarnings)
	}
	if settings.AutoloadProjectConfigEnabled() {
		t.Fatalf("autoloadProjectConfig = %#v", settings.AutoloadProjectConfig)
	}
	if settings.Debug == nil || settings.DebugEnabled() {
		t.Fatalf("debug = %#v", settings.Debug)
	}
	if settings.Yolo == nil || !settings.YoloEnabled() {
		t.Fatalf("yolo = %#v", settings.Yolo)
	}
}

func TestLoadParsesContainerDefaults(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "toby")
	writeFile(t, filepath.Join(dir, "config.yaml"), []byte(`
container:
  image: mcr.microsoft.com/devcontainers/javascript-node:24-bookworm
  build:
    context: docker/context
    dockerfile: ../Dockerfile.toby
settings:
  suppressWarnings:
    - opencode.model-discovery
  autoloadProjectConfig: true
  debug: true
  yolo: true
`))

	cfg, err := Load(dir, home)
	if err != nil {
		t.Fatal(err)
	}
	container := cfg.Container()
	if container.Image != "mcr.microsoft.com/devcontainers/javascript-node:24-bookworm" {
		t.Fatalf("container = %#v", container)
	}
	if container.Build.Context != filepath.Join(dir, "docker", "context") || container.Build.Dockerfile != filepath.Join(dir, "docker", "Dockerfile.toby") {
		t.Fatalf("container build = %#v", container.Build)
	}
	settings := cfg.Settings()
	if !settings.SuppressWarnings.Suppresses(warning.OpenCodeModelDiscovery) || settings.SuppressWarnings.Suppresses(warning.MountHostBacking) {
		t.Fatalf("suppress warnings = %#v", settings.SuppressWarnings)
	}
	if !settings.AutoloadProjectConfigEnabled() {
		t.Fatalf("autoloadProjectConfig = %#v", settings.AutoloadProjectConfig)
	}
	if !settings.DebugEnabled() {
		t.Fatalf("debug = %#v", settings.Debug)
	}
	if !settings.YoloEnabled() {
		t.Fatalf("yolo = %#v", settings.Yolo)
	}
}

func TestLoadParsesMCPImageAndServers(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "toby")
	writeFile(t, filepath.Join(dir, "config.yaml"), []byte(`
mcp:
  image: ghcr.io/acme/mcp:latest
  server:
    docs:
      type: local
      transport: http
      image: ghcr.io/acme/docs:latest
      command: [docs-mcp, --port, "3000"]
      port: 3000
      path: mcp
`))

	cfg, err := Load(dir, home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MCPImage() != "ghcr.io/acme/mcp:latest" {
		t.Fatalf("mcp image = %q", cfg.MCPImage())
	}
	docs := cfg.MCPServers()["docs"]
	if docs.Image() != "ghcr.io/acme/docs:latest" {
		t.Fatalf("docs image = %q", docs.Image())
	}
	if docs.Transport() != MCPTransportHTTP || docs.Port() != 3000 || docs.Path() != "/mcp" {
		t.Fatalf("docs transport = %q port=%d path=%q", docs.Transport(), docs.Port(), docs.Path())
	}
	command, err := docs.CommandParts()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(command, []string{"docs-mcp", "--port", "3000"}) {
		t.Fatalf("command = %#v", command)
	}
}

func TestLoadRejectsInvalidSuppressedWarning(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "toby")
	writeFile(t, filepath.Join(dir, "config.yaml"), []byte(`
settings:
  suppressWarnings:
    - unknown.warning
`))

	if _, err := Load(dir, home); err == nil {
		t.Fatal("expected invalid suppressed warning to fail")
	}
}

func TestLoadRejectsUnsupportedProviderType(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "toby")
	writeFile(t, filepath.Join(dir, "config.yaml"), []byte(`
provider:
  local:
    type: bedrock
    baseURL: https://example.com/v1
`))

	if _, err := Load(dir, home); err == nil {
		t.Fatal("expected unsupported provider type to fail")
	}
}

func TestLoadRejectsUnsupportedTopLevelKeys(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "toby")
	writeFile(t, filepath.Join(dir, "config.yaml"), []byte(`
autoupdate: true
`))

	if _, err := Load(dir, home); err == nil {
		t.Fatal("expected unsupported key to fail")
	}
}

func TestRegisterContextFilesUsesBasenameAndRandomizesCollisions(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "toby")
	first := filepath.Join(dir, "a", "foobar.md")
	second := filepath.Join(dir, "b", "foobar.md")
	writeFile(t, first, []byte("first"))
	writeFile(t, second, []byte("second"))
	writeFile(t, filepath.Join(dir, "config.yaml"), []byte(`
instruction:
  - a/foobar.md
  - b/foobar.md
`))
	cfg, err := Load(dir, home)
	if err != nil {
		t.Fatal(err)
	}
	service := contextfiles.NewService()
	sandbox := &configFakeSandbox{contextDir: "/run/user/1000/toby/context"}
	service.SetSandbox(sandbox)

	if err := cfg.RegisterContextFiles(context.Background(), service); err != nil {
		t.Fatal(err)
	}
	instructions := service.InstructionPaths()
	if len(instructions) != 2 {
		t.Fatalf("instructions = %#v", instructions)
	}
	files := sandbox.files
	if len(files) != 2 {
		t.Fatalf("files = %#v", files)
	}
	if files[0].Path != "instructions/foobar.md" || string(files[0].Data) != "first" {
		t.Fatalf("first file = %#v", files[0])
	}
	matched, err := regexp.MatchString(`^instructions/foobar\.[0-9a-f]{6}\.md$`, files[1].Path)
	if err != nil {
		t.Fatal(err)
	}
	if !matched || string(files[1].Data) != "second" {
		t.Fatalf("second file = %#v", files[1])
	}
	if filepath.Base(instructions[1]) == "foobar.md" {
		t.Fatalf("collision context path was not randomized: %#v", instructions[1])
	}
}

func TestMCPServerHeadersCloneAndResolve(t *testing.T) {
	t.Setenv("MCP_TOKEN", "env-token")
	server := MCPServer{raw: map[string]any{
		"enabled": false,
		"type":    "remote",
		"url":     " https://example.com/mcp ",
		"headers": map[string]any{
			"X-List":        []any{"a", "b"},
			"X-Keep":        "base",
			"Authorization": "Bearer {env:MCP_TOKEN}",
		},
	}}

	raw := server.Raw()
	raw["url"] = "mutated"
	if server.URL() != "https://example.com/mcp" {
		t.Fatalf("server URL mutated through Raw clone: %q", server.URL())
	}
	if server.Enabled() {
		t.Fatal("server should be disabled")
	}
	if !server.HTTPProxyable() {
		t.Fatal("remote server should be HTTP proxyable")
	}
	headers, err := server.Headers()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := headers.Values("X-List"), []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("X-List = %#v, want %#v", got, want)
	}
	if headers.Get("X-Keep") != "base" || headers.Get("Authorization") != "Bearer env-token" {
		t.Fatalf("headers = %#v", headers)
	}
}

func TestMCPServerHTTPProxyableCases(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want bool
	}{
		{name: "remote", raw: map[string]any{"type": "remote", "command": "ignored"}, want: true},
		{name: "implicit url", raw: map[string]any{"url": " https://example.com/mcp "}, want: true},
		{name: "implicit command", raw: map[string]any{"command": "mcp"}, want: true},
		{name: "local", raw: map[string]any{"type": "local", "command": "mcp"}, want: true},
		{name: "blank", raw: map[string]any{"url": " "}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MCPServerHTTPProxyable(tt.raw); got != tt.want {
				t.Fatalf("MCPServerHTTPProxyable = %v, want %v", got, tt.want)
			}
		})
	}
	if (MCPServer{}).Enabled() != true {
		t.Fatal("missing enabled should default true")
	}
}

func TestMCPServerHeadersRejectInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{name: "headers not object", raw: map[string]any{"headers": []any{"bad"}}, want: "headers: must be an object"},
		{name: "header item not string", raw: map[string]any{"headers": map[string]any{"X": []any{"ok", 1}}}, want: `headers: header "X" entries must be strings`},
		{name: "header value invalid", raw: map[string]any{"headers": map[string]any{"X": 1}}, want: `headers: header "X" value must be a string or string array`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := (MCPServer{raw: tt.raw}).Headers()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestResolveStringSubstitutions(t *testing.T) {
	home := t.TempDir()
	firstDir := filepath.Join(home, "first")
	secondDir := filepath.Join(home, "second")
	writeFile(t, filepath.Join(secondDir, "secret.txt"), []byte(" relative-secret\n"))
	writeFile(t, filepath.Join(home, "home-secret.txt"), []byte("home-secret\n"))
	absPath := filepath.Join(home, "absolute.txt")
	writeFile(t, absPath, []byte(" absolute-secret\n"))
	t.Setenv("API_TOKEN", "env-secret")

	resolved, err := resolveString("env={env:API_TOKEN}; rel={file:secret.txt}; home={file:~/home-secret.txt}; abs={file:"+absPath+"}", []string{firstDir, secondDir}, home)
	if err != nil {
		t.Fatal(err)
	}
	want := "env=env-secret; rel=relative-secret; home=home-secret; abs=absolute-secret"
	if resolved != want {
		t.Fatalf("resolved = %q, want %q", resolved, want)
	}

	_, err = resolveString("missing={file:missing.txt}", []string{firstDir}, home)
	if err == nil || !strings.Contains(err.Error(), `unable to read file substitution "missing.txt"`) {
		t.Fatalf("err = %v", err)
	}

	if _, err := readSubstitutionFile("secret.txt", []string{"", secondDir}); err != nil {
		t.Fatalf("readSubstitutionFile skipped empty dirs: %v", err)
	}
	if _, err := (MCPServer{raw: map[string]any{"headers": http.Header{"X": []string{"bad"}}}}).Headers(); err == nil {
		t.Fatal("expected http.Header input to be rejected because config values must be plain objects")
	}
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

type configFakeSandbox struct {
	contextDir string
	files      []contextfiles.File
}

func (s *configFakeSandbox) ProjectPath(string) (string, bool)                    { return "", false }
func (s *configFakeSandbox) VisibleHostPath(string) (string, error)               { return "", nil }
func (s *configFakeSandbox) GetEnvironment(string) (string, bool)                 { return "", false }
func (s *configFakeSandbox) SetEnvironment(context.Context, string, string) error { return nil }
func (s *configFakeSandbox) PrependEnvironment(context.Context, string, string, string) error {
	return nil
}
func (s *configFakeSandbox) AppendEnvironment(context.Context, string, string, string) error {
	return nil
}
func (s *configFakeSandbox) AddBind(mount.Bind) error { return nil }
func (s *configFakeSandbox) AddMount(req mount.Request) (mount.Entry, error) {
	return mount.Entry{Key: req.Key}, nil
}
func (s *configFakeSandbox) Mount(mount.Key) (mount.Entry, bool) {
	return mount.Entry{}, false
}
func (s *configFakeSandbox) AddFile(_ context.Context, path string, data []byte, mode uint32) error {
	rel := strings.TrimPrefix(path, layout.Context+string(os.PathSeparator))
	s.files = append(s.files, contextfiles.File{Path: filepath.ToSlash(rel), Data: append([]byte(nil), data...), Mode: mode})
	return nil
}
func (s *configFakeSandbox) AddFileOwned(ctx context.Context, path string, data []byte, mode uint32, _, _ int) error {
	return s.AddFile(ctx, path, data, mode)
}
func (s *configFakeSandbox) DeletePath(context.Context, string, bool) error { return nil }
func (s *configFakeSandbox) Mkdir(context.Context, string, uint32) error    { return nil }
func (s *configFakeSandbox) MkdirOwned(ctx context.Context, path string, mode uint32, _, _ int) error {
	return s.Mkdir(ctx, path, mode)
}
func (s *configFakeSandbox) Symlink(context.Context, string, string) error { return nil }
func (s *configFakeSandbox) SymlinkOwned(ctx context.Context, path, target string, _, _ int) error {
	return s.Symlink(ctx, path, target)
}
func (s *configFakeSandbox) Exec(context.Context, []string, sandboxapi.ExecOptions) (int, error) {
	return 0, nil
}
func (s *configFakeSandbox) TobyMCPURL() string { return "" }
