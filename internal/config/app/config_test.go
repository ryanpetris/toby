package appconfig

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"petris.dev/toby/container/mount"
	"petris.dev/toby/context/files"
	"petris.dev/toby/diagnostic/warning"
	sandboxapi "petris.dev/toby/sandbox"
)

func TestLoadDeepMergesConfigFiles(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "toby")
	writeFile(t, filepath.Join(dir, "config.json"), []byte(`{
  "mcps": {
    "servers": {
      "docs": { "type": "remote", "url": "https://example.com/mcp" }
    }
  },
  "instructions": ["base.md"],
  "providers": {
    "servers": {
      "local": { "type": "openai", "headers": { "Authorization": "Bearer base" } }
    }
  },
  "permissions": {
    "paths": {
      "~/allowed": "allow",
      "~/allowed/**": "allow"
    }
  },
  "settings": {
    "homeProfile": "default",
    "suppressWarnings": ["provider.model-discovery"],
    "autoloadProjectConfig": true,
    "debug": true,
    "yolo": false
  },
  "container": { "image": "node:base" }
}`))
	writeFile(t, filepath.Join(dir, "config.yaml"), []byte(`
instructions:
  - base.md
  - extra.md
mcps:
  servers:
    docs:
      enabled: true
providers:
  servers:
    local:
      url: https://models.example.com
permissions:
  paths:
    /tmp/shared: allow
settings:
  suppressWarnings:
    - mount.host-backing
  autoloadProjectConfig: false
  debug: false
  yolo: true
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
	// instruction lists union across files (dedup, first-occurrence order): json's
	// [base.md] plus yaml's [base.md, extra.md] => [base.md, extra.md].
	instructions := cfg.Instructions()
	if len(instructions) != 2 || instructions[0] != "base.md" || instructions[1] != "extra.md" {
		t.Fatalf("instructions = %#v", instructions)
	}
	provider := cfg.Providers()["local"]
	if provider.Type != ProviderTypeOpenAI || provider.Headers["Authorization"] != "Bearer base" || provider.URL != "https://models.example.com" {
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
	settings := cfg.Settings()
	if settings.HomeProfile != "default" || cfg.HomeProfile() != "default" {
		t.Fatalf("home profile = %q", settings.HomeProfile)
	}
	// suppressWarnings lists union across files: json's [provider.model-discovery]
	// plus yaml's [mount.host-backing] suppresses both.
	if !settings.SuppressWarnings.Suppresses(warning.MountHostBacking) || !settings.SuppressWarnings.Suppresses(warning.ModelDiscovery) {
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
    - provider.model-discovery
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
	if !settings.SuppressWarnings.Suppresses(warning.ModelDiscovery) || settings.SuppressWarnings.Suppresses(warning.MountHostBacking) {
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

func TestPermissionPathsPassesThroughVerbatimAndYoloRoot(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "toby")
	writeFile(t, filepath.Join(dir, "config.yaml"), []byte(`
permissions:
  paths:
    /verbatim: allow
    /subtree/: allow
`))
	cfg, err := Load(dir, home)
	if err != nil {
		t.Fatal(err)
	}

	paths := cfg.PermissionPaths()
	// Default directories are passed through verbatim, with no glob or trailing
	// slash added.
	if paths["/tmp"] != defaultPermissionMode {
		t.Fatalf("default /tmp should be present verbatim: %#v", paths)
	}
	if _, ok := paths["/tmp/"]; ok {
		t.Fatalf("default paths should not gain a trailing slash: %#v", paths)
	}
	if _, ok := paths["/tmp/**"]; ok {
		t.Fatalf("default paths should not contain a recursive glob: %#v", paths)
	}
	// User entries pass through verbatim, preserving exactly what they wrote.
	if paths["/verbatim"] != "allow" || paths["/subtree/"] != "allow" {
		t.Fatalf("user permission paths = %#v", paths)
	}
	if _, ok := paths["/"]; ok {
		t.Fatalf("root should not be granted without yolo: %#v", paths)
	}

	// yolo folded into the config grants the filesystem root.
	enabled := true
	yoloed := cfg.WithOverrides(LaunchOverrides{Yolo: &enabled})
	if yoloed.PermissionPaths()["/"] != defaultPermissionMode {
		t.Fatalf("yolo should grant the filesystem root: %#v", yoloed.PermissionPaths())
	}
	// The override must not mutate the original Service.
	if _, ok := cfg.PermissionPaths()["/"]; ok {
		t.Fatalf("WithOverrides must not mutate the receiver: %#v", cfg.PermissionPaths())
	}
}

func TestWithOverridesPrecedenceAndMerges(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "toby")
	writeFile(t, filepath.Join(dir, "config.yaml"), []byte(`
settings:
  homeProfile: base
  suppressWarnings:
    - mount.host-backing
container:
  image: node:host
`))
	cfg, err := Load(dir, home)
	if err != nil {
		t.Fatal(err)
	}

	debug := true
	effective := cfg.WithOverrides(LaunchOverrides{
		Image:            "node:launch",
		HomeProfile:      "launch",
		Debug:            &debug,
		SuppressWarnings: warning.Suppression{Set: true, IDs: map[warning.ID]bool{warning.ModelDiscovery: true}},
	})

	// Scalar overrides win.
	if effective.Image() != "node:launch" {
		t.Fatalf("image = %q", effective.Image())
	}
	if effective.HomeProfile() != "launch" {
		t.Fatalf("home profile = %q", effective.HomeProfile())
	}
	if !effective.DebugEnabled() {
		t.Fatalf("debug should be enabled")
	}
	// Suppressed warnings union the config base with the launch override.
	suppress := effective.Settings().SuppressWarnings
	if !suppress.Suppresses(warning.ModelDiscovery) || !suppress.Suppresses(warning.MountHostBacking) {
		t.Fatalf("suppress should union config and override: %#v", suppress)
	}
	// The receiver is unchanged.
	if cfg.Image() != "node:host" || cfg.HomeProfile() != "base" {
		t.Fatalf("receiver mutated: image=%q profile=%q", cfg.Image(), cfg.HomeProfile())
	}
}

func TestLoadParsesMCPServers(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "toby")
	writeFile(t, filepath.Join(dir, "config.yaml"), []byte(`
mcps:
  servers:
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

func TestLoadMCPImageAndBuildDefaults(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "toby")
	writeFile(t, filepath.Join(dir, "config.yaml"), []byte(`
mcps:
  image: ghcr.io/acme/mcp-base:latest
  build:
    context: docker/mcp
  servers:
    docs:
      type: local
      command: docs-mcp
`))
	cfg, err := Load(dir, home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MCPImage() != "ghcr.io/acme/mcp-base:latest" {
		t.Fatalf("mcp image = %q", cfg.MCPImage())
	}
	build := cfg.MCPBuild()
	if build.Context != filepath.Join(dir, "docker", "mcp") || build.Dockerfile != filepath.Join(dir, "docker", "mcp", "Dockerfile") {
		t.Fatalf("mcp build = %#v", build)
	}
	if _, ok := cfg.MCPServers()["docs"]; !ok {
		t.Fatalf("docs server missing from %#v", cfg.MCPServers())
	}
}

func TestLoadRejectsProviderEntriesWithoutServers(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "toby")
	// Provider entries must live under `providers.servers`; a flat entry directly
	// under `providers` is an unknown key and strict decoding rejects it.
	writeFile(t, filepath.Join(dir, "config.yaml"), []byte(`
providers:
  local:
    type: openai
    url: https://example.com/v1
`))
	if _, err := Load(dir, home); err == nil {
		t.Fatal("expected provider entry without servers to be rejected")
	}
}

func TestLoadRejectsUnsupportedProviderType(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "toby")
	writeFile(t, filepath.Join(dir, "config.yaml"), []byte(`
providers:
  servers:
    local:
      type: bedrock
      url: https://example.com/v1
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
instructions:
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
	if files[0].Path != "/toby/home/.toby/instructions/foobar.md" || string(files[0].Data) != "first" {
		t.Fatalf("first file = %#v", files[0])
	}
	matched, err := regexp.MatchString(`^/toby/home/\.toby/instructions/foobar\.[0-9a-f]{6}\.md$`, files[1].Path)
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

func TestMCPServerEnabledDefaultsTrue(t *testing.T) {
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
func (s *configFakeSandbox) Environment(string) (string, bool)                    { return "", false }
func (s *configFakeSandbox) SetEnvironment(context.Context, string, string) error { return nil }
func (s *configFakeSandbox) PrependEnvironment(context.Context, string, string, string) error {
	return nil
}
func (s *configFakeSandbox) AppendEnvironment(context.Context, string, string, string) error {
	return nil
}
func (s *configFakeSandbox) AddBind(mount.Bind) error { return nil }
func (s *configFakeSandbox) AddFile(_ context.Context, path string, data []byte, mode uint32) error {
	s.files = append(s.files, contextfiles.File{Path: filepath.ToSlash(path), Data: append([]byte(nil), data...), Mode: mode})
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

func (s *configFakeSandbox) ProxyBaseURL(id string) string { return "http://127.0.0.1:0/proxy/" + id }
