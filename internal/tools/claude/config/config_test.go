package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"petris.dev/toby/internal/config/toby"
	"petris.dev/toby/internal/context/files"
	"petris.dev/toby/internal/control/httpproxy"
	"petris.dev/toby/internal/control/mcpproxy"
	sandboxpath "petris.dev/toby/internal/sandbox/path"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
)

const testHome = "/toby/home"

const testTobyMCPURL = "http://127.0.0.1:12345/proxy/toby"

func fileByPath(t *testing.T, files []contextfiles.File, path string) contextfiles.File {
	t.Helper()
	for _, file := range files {
		if file.Path == path {
			return file
		}
	}
	t.Fatalf("static file %q not found", path)
	return contextfiles.File{}
}

func decode(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	return value
}

func TestContextFilesIncludesTobyMCPServer(t *testing.T) {
	files, err := renderContextFiles(t, "/home/toby/Projects/app", [][]byte{[]byte("# git")})
	if err != nil {
		t.Fatal(err)
	}

	mcp := decode(t, fileByPath(t, files, StaticMcpPath).Data)
	toby := mcp["mcpServers"].(map[string]any)["toby"].(map[string]any)
	if toby["type"] != "http" || toby["url"] != testTobyMCPURL {
		t.Fatalf("mcp.toby = %#v", toby)
	}
}

func TestContextFilesIncludesConfiguredMCPServers(t *testing.T) {
	cfg := testTobyConfig(t, []byte(`
mcps:
  docs:
    type: local
    command: [npx, -y, docs-mcp]
    environment:
      TOKEN: abc
  remote:
    type: remote
    url: https://example.com/mcp
`))
	files, err := renderContextFilesWithConfig(t, "/home/toby/Projects/app", [][]byte{[]byte("# git")}, cfg)
	if err != nil {
		t.Fatal(err)
	}

	mcp := decode(t, fileByPath(t, files, StaticMcpPath).Data)
	servers := mcp["mcpServers"].(map[string]any)
	docs := servers["docs"].(map[string]any)
	if docs["type"] != "http" {
		t.Fatalf("docs mcp = %#v", docs)
	}
	if url, _ := docs["url"].(string); !strings.HasPrefix(url, "http://127.0.0.1:12345/proxy/") {
		t.Fatalf("docs url = %#v", docs["url"])
	}
	remote := servers["remote"].(map[string]any)
	if remote["type"] != "http" {
		t.Fatalf("remote mcp = %#v", remote)
	}
	if url, _ := remote["url"].(string); !strings.HasPrefix(url, "http://127.0.0.1:12345/proxy/") {
		t.Fatalf("remote url = %#v", remote["url"])
	}
}

func TestContextFilesIncludesPermissionDirectories(t *testing.T) {
	projectRoot := "/toby/workspace"
	files, err := renderContextFiles(t, projectRoot, [][]byte{[]byte("# git")})
	if err != nil {
		t.Fatal(err)
	}

	settings := decode(t, fileByPath(t, files, StaticSettingsPath).Data)
	dirs := settings["permissions"].(map[string]any)["additionalDirectories"].([]any)
	got := map[string]bool{}
	for _, dir := range dirs {
		got[dir.(string)] = true
	}
	for _, want := range []string{projectRoot, "/tmp", testHome + "/go", testHome + "/.cache"} {
		if !got[want] {
			t.Fatalf("additionalDirectories missing %q: %#v", want, dirs)
		}
	}
	for _, dir := range dirs {
		if strings.Contains(dir.(string), "*") {
			t.Fatalf("additionalDirectories should not contain glob patterns: %q", dir)
		}
	}
}

func TestContextFilesPermissionUserOverride(t *testing.T) {
	cfg := testTobyConfig(t, []byte(`
permissions:
  paths:
    /tmp: deny
    /custom: allow
`))
	files, err := renderContextFilesWithConfig(t, "/toby/workspace", [][]byte{[]byte("# git")}, cfg)
	if err != nil {
		t.Fatal(err)
	}

	settings := decode(t, fileByPath(t, files, StaticSettingsPath).Data)
	dirs := settings["permissions"].(map[string]any)["additionalDirectories"].([]any)
	got := map[string]bool{}
	for _, dir := range dirs {
		got[dir.(string)] = true
	}
	if got["/tmp"] {
		t.Fatalf("user deny should drop /tmp from additionalDirectories: %#v", dirs)
	}
	if !got["/custom"] {
		t.Fatalf("user-configured /custom missing: %#v", dirs)
	}
}

func TestContextFilesCombinesInstructions(t *testing.T) {
	files, err := renderContextFiles(t, "/p", [][]byte{[]byte("# git\n"), []byte("# context\n")})
	if err != nil {
		t.Fatal(err)
	}
	got := string(fileByPath(t, files, StaticInstructionsPath).Data)
	if !strings.Contains(got, "# git") || !strings.Contains(got, "# context") {
		t.Fatalf("instructions = %q", got)
	}
}

func TestContextFilesDoNotIncludePlugin(t *testing.T) {
	files, err := renderContextFiles(t, "/p", [][]byte{[]byte("# git")})
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasPrefix(file.Path, "claude/plugin/") {
			t.Fatalf("unexpected plugin file: %q", file.Path)
		}
	}
}

func renderContextFiles(t *testing.T, projectRoot string, instructions [][]byte) ([]contextfiles.File, error) {
	return renderContextFilesWithConfig(t, projectRoot, instructions, nil)
}

func renderContextFilesWithConfig(t *testing.T, projectRoot string, instructions [][]byte, cfg *tobyconfig.Service) ([]contextfiles.File, error) {
	t.Helper()
	var service *contextfiles.Service
	app := fxtest.New(t,
		fx.Provide(contextfiles.NewService),
		fx.Populate(&service),
	)
	app.RequireStart()
	t.Cleanup(app.RequireStop)
	builder := service.NewBuilder()
	proxy := httpproxy.NewService(httpproxy.ServiceParams{})
	mcpProxy, err := mcpproxy.NewService(mcpproxy.ServiceParams{Proxy: proxy, Runtimes: []mcpproxy.Runtime{mcpproxy.NewDockerRunner(), mcpproxy.NewBubblewrapRunner()}})
	if err != nil {
		return nil, err
	}
	if cfg != nil {
		if err := mcpProxy.Configure(t.Context(), "127.0.0.1:12345", cfg, mcpproxy.Defaults{}); err != nil {
			return nil, err
		}
	}
	paths := sandboxpath.Paths{Home: testHome, Workspace: projectRoot}
	if err := RegisterContextFiles(builder, paths, instructions, cfg, "127.0.0.1:12345", testTobyMCPURL, proxy, mcpProxy); err != nil {
		return nil, err
	}
	return builder.Files(), nil
}

func testTobyConfig(t *testing.T, data []byte) *tobyconfig.Service {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := tobyconfig.Load(dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
