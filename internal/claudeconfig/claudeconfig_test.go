package claudeconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"petris.dev/toby/internal/contextfiles"
	"petris.dev/toby/internal/tobyconfig"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
)

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
	if toby["type"] != "stdio" || toby["command"] != "toby" {
		t.Fatalf("mcp.toby = %#v", toby)
	}
	if args := toby["args"].([]any); len(args) != 2 || args[0] != "sandbox" || args[1] != "mcp" {
		t.Fatalf("mcp.toby.args = %#v", args)
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
	if remote["type"] != "http" || remote["url"] != "https://example.com/mcp" {
		t.Fatalf("remote mcp = %#v", remote)
	}
}

func TestContextFilesIncludesPermissionDirectories(t *testing.T) {
	projectRoot := "/home/toby/Projects/app"
	files, err := renderContextFiles(t, projectRoot, [][]byte{[]byte("# git")})
	if err != nil {
		t.Fatal(err)
	}

	settings := decode(t, fileByPath(t, files, StaticSettingsPath).Data)
	dirs := settings["permissions"].(map[string]any)["additionalDirectories"].([]any)
	want := map[string]bool{"/tmp": false, projectRoot: false}
	for _, dir := range dirs {
		if _, ok := want[dir.(string)]; ok {
			want[dir.(string)] = true
		}
	}
	for dir, seen := range want {
		if !seen {
			t.Fatalf("additionalDirectories missing %q: %#v", dir, dirs)
		}
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
	if err := RegisterContextFiles(builder, projectRoot, instructions, cfg); err != nil {
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
