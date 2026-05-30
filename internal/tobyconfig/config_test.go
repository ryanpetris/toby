package tobyconfig

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"petris.dev/toby/internal/contextfiles"
	"petris.dev/toby/internal/tool"
	"petris.dev/toby/internal/warning"
)

func TestLoadDeepMergesConfigFiles(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "toby")
	writeFile(t, filepath.Join(dir, "config.jsonc"), []byte(`{
  // JSONC is accepted.
  "mcp": {
    "docs": { "type": "remote", "url": "https://example.com/mcp" },
  },
  "instructions": ["base.md"],
  "provider": {
      "local": { "type": "openai", "headers": { "Authorization": "Bearer base" } }
    },
  "permission": {
    "paths": {
      "~/allowed": "allow",
      "~/allowed/**": "allow"
    }
  },
  "sandbox": {
    "runtime": {
      "default": "bubblewrap",
      "docker": { "image": "node:base", "home": "/home/base" },
      "bubblewrap": { "root": "sandboxes/base" }
    },
    "tools": { "default": { "state": "host", "stateRoot": "~/state/default" }, "opencode": { "state": "private" } },
    "suppressWarnings": true,
    "autoloadProjectConfig": true
  },
}`))
	writeFile(t, filepath.Join(dir, "config.yaml"), []byte(`
mcp:
  docs:
    enabled: true
instructions:
  - extra.md
provider:
  local:
    baseURL: https://models.example.com
permission:
  paths:
    /tmp/shared: allow
sandbox:
  runtime:
    default: docker
    docker:
      image: node:custom
      projects: /workspace/custom
      build: {}
  tools:
    claude:
      state: host
      stateRoot: state/claude
  suppressWarnings:
    - tool.host-state
  autoloadProjectConfig: false
`))

	cfg, err := Load(dir, home)
	if err != nil {
		t.Fatal(err)
	}
	mcp := cfg.MCPServers()["docs"].Raw()
	if mcp["url"] != "https://example.com/mcp" || mcp["enabled"] != true {
		t.Fatalf("mcp.docs = %#v", mcp)
	}
	instructions := cfg.Instructions()
	if len(instructions) != 2 || instructions[0] != "base.md" || instructions[1] != "extra.md" {
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
	sandbox := cfg.Sandbox()
	if sandbox.Runtime.Default != "docker" || sandbox.Runtime.Docker.Image != "node:custom" || sandbox.Runtime.Docker.Home != "/home/base" || sandbox.Runtime.Docker.Projects != "/workspace/custom" {
		t.Fatalf("sandbox = %#v", sandbox)
	}
	if sandbox.Runtime.Docker.Build.Context != dir || sandbox.Runtime.Docker.Build.Dockerfile != filepath.Join(dir, "Dockerfile") {
		t.Fatalf("docker build = %#v", sandbox.Runtime.Docker.Build)
	}
	if sandbox.Runtime.Bubblewrap.Root != filepath.Join(dir, "sandboxes", "base") {
		t.Fatalf("bubblewrap = %#v", sandbox.Runtime.Bubblewrap)
	}
	if sandbox.Tools.Default.State != tool.ToolStateHost || sandbox.Tools.StateFor("opencode") != tool.ToolStatePrivate || sandbox.Tools.StateFor("claude") != tool.ToolStateHost {
		t.Fatalf("sandbox tools = %#v", sandbox.Tools)
	}
	if sandbox.Tools.StateRootFor("opencode") != filepath.Join(home, "state", "default") || sandbox.Tools.StateRootFor("claude") != filepath.Join(dir, "state", "claude") {
		t.Fatalf("sandbox tool roots = %#v", sandbox.Tools)
	}
	if !sandbox.SuppressWarnings.Suppresses(warning.ToolHostState) || sandbox.SuppressWarnings.Suppresses(warning.OpenCodeModelDiscovery) {
		t.Fatalf("suppress warnings = %#v", sandbox.SuppressWarnings)
	}
	if sandbox.AutoloadProjectConfigEnabled() {
		t.Fatalf("autoloadProjectConfig = %#v", sandbox.AutoloadProjectConfig)
	}
}

func TestLoadParsesSandboxDefaults(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "toby")
	writeFile(t, filepath.Join(dir, "config.yaml"), []byte(`
sandbox:
  runtime:
    default: docker
    docker:
      image: node:lts-bookworm
      home: /home/toby
      projects: /workspace
      build:
        context: docker/context
        dockerfile: ../Dockerfile.toby
    bubblewrap:
      root: ~/sandboxes
  tools:
    default:
      state: private
      stateRoot: ~/unused
    opencode:
      state: host
      stateRoot: /tmp/opencode-state
  suppressWarnings:
    - opencode.model-discovery
  autoloadProjectConfig: true
`))

	cfg, err := Load(dir, home)
	if err != nil {
		t.Fatal(err)
	}
	sandbox := cfg.Sandbox()
	if sandbox.Runtime.Default != "docker" || sandbox.Runtime.Docker.Image != "node:lts-bookworm" || sandbox.Runtime.Docker.Home != "/home/toby" || sandbox.Runtime.Docker.Projects != "/workspace" {
		t.Fatalf("sandbox = %#v", sandbox)
	}
	if sandbox.Runtime.Docker.Build.Context != filepath.Join(dir, "docker", "context") || sandbox.Runtime.Docker.Build.Dockerfile != filepath.Join(dir, "docker", "Dockerfile.toby") {
		t.Fatalf("docker build = %#v", sandbox.Runtime.Docker.Build)
	}
	if sandbox.Runtime.Bubblewrap.Root != filepath.Join(home, "sandboxes") {
		t.Fatalf("bubblewrap = %#v", sandbox.Runtime.Bubblewrap)
	}
	if sandbox.Tools.Default.State != tool.ToolStatePrivate || sandbox.Tools.StateFor("opencode") != tool.ToolStateHost {
		t.Fatalf("sandbox tools = %#v", sandbox.Tools)
	}
	if sandbox.Tools.StateRootFor("opencode") != "/tmp/opencode-state" {
		t.Fatalf("sandbox tool roots = %#v", sandbox.Tools)
	}
	if !sandbox.SuppressWarnings.Suppresses(warning.OpenCodeModelDiscovery) || sandbox.SuppressWarnings.Suppresses(warning.ToolHostState) {
		t.Fatalf("suppress warnings = %#v", sandbox.SuppressWarnings)
	}
	if !sandbox.AutoloadProjectConfigEnabled() {
		t.Fatalf("autoloadProjectConfig = %#v", sandbox.AutoloadProjectConfig)
	}
}

func TestLoadRejectsInvalidToolState(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "toby")
	writeFile(t, filepath.Join(dir, "config.yaml"), []byte(`
sandbox:
  tools:
    opencode:
      state: shared
`))

	if _, err := Load(dir, home); err == nil {
		t.Fatal("expected invalid tool state to fail")
	}
}

func TestLoadRejectsInvalidSuppressedWarning(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "toby")
	writeFile(t, filepath.Join(dir, "config.yaml"), []byte(`
sandbox:
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
instructions:
  - a/foobar.md
  - b/foobar.md
`))
	cfg, err := Load(dir, home)
	if err != nil {
		t.Fatal(err)
	}
	service := contextfiles.NewService()
	session := service.NewSession("/run/user/1000/toby/context")

	if err := cfg.RegisterContextFiles(session); err != nil {
		t.Fatal(err)
	}
	instructions := session.InstructionPaths()
	if len(instructions) != 2 {
		t.Fatalf("instructions = %#v", instructions)
	}
	files := session.Files()
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

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
