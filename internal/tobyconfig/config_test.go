package tobyconfig

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"petris.dev/toby/internal/contextfiles"
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
    "local": { "npm": "@ai-sdk/openai-compatible", "options": { "apiKey": "base" } }
  },
  "sandbox": {
    "runtime": "bubblewrap",
    "docker": { "image": "node:base", "home": "/home/base" }
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
    options:
      baseURL: https://models.example.com
sandbox:
  runtime: docker
  docker:
    image: node:custom
    projects: /workspace/custom
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
	options := cfg.Providers()["local"].Raw()["options"].(map[string]any)
	if options["apiKey"] != "base" || options["baseURL"] != "https://models.example.com" {
		t.Fatalf("provider options = %#v", options)
	}
	sandbox := cfg.Sandbox()
	if sandbox.Runtime != "docker" || sandbox.Docker.Image != "node:custom" || sandbox.Docker.Home != "/home/base" || sandbox.Docker.Projects != "/workspace/custom" {
		t.Fatalf("sandbox = %#v", sandbox)
	}
}

func TestLoadParsesSandboxDefaults(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "toby")
	writeFile(t, filepath.Join(dir, "config.yaml"), []byte(`
sandbox:
  runtime: docker
  docker:
    image: node:lts-bookworm
    home: /home/toby
    projects: /workspace
`))

	cfg, err := Load(dir, home)
	if err != nil {
		t.Fatal(err)
	}
	sandbox := cfg.Sandbox()
	if sandbox.Runtime != "docker" || sandbox.Docker.Image != "node:lts-bookworm" || sandbox.Docker.Home != "/home/toby" || sandbox.Docker.Projects != "/workspace" {
		t.Fatalf("sandbox = %#v", sandbox)
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
