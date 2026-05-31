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

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
)

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
mcp:
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
	if docs["type"] != "stdio" || docs["command"] != "npx" {
		t.Fatalf("docs mcp = %#v", docs)
	}
	args := docs["args"].([]any)
	if len(args) != 2 || args[0] != "-y" || args[1] != "docs-mcp" {
		t.Fatalf("docs args = %#v", args)
	}
	remote := servers["remote"].(map[string]any)
	if remote["type"] != "http" {
		t.Fatalf("remote mcp = %#v", remote)
	}
	if url, _ := remote["url"].(string); !strings.HasPrefix(url, "http://127.0.0.1:12345/proxy/") {
		t.Fatalf("remote url = %#v", remote["url"])
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
	if err := RegisterContextFiles(builder, projectRoot, instructions, cfg, "127.0.0.1:12345", testTobyMCPURL, httpproxy.NewService(httpproxy.ServiceParams{})); err != nil {
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
