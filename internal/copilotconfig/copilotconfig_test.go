package copilotconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"petris.dev/toby/internal/contextfiles"
	"petris.dev/toby/internal/httpproxy"
	"petris.dev/toby/internal/tobyconfig"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
)

const testTobyMCPURL = "http://127.0.0.1:12345/proxy/toby"

func TestContextFilesIncludeTobyMCPAndInstructions(t *testing.T) {
	files, err := renderContextFiles(t, [][]byte{[]byte("# git\n"), []byte("# extra\n")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	mcp := decodeMCP(t, fileByPath(t, files, StaticMCPPath).Data)
	toby := mcp["mcpServers"].(map[string]any)["toby"].(map[string]any)
	if toby["type"] != "http" || toby["url"] != testTobyMCPURL {
		t.Fatalf("toby server = %#v", toby)
	}
	if got := string(fileByPath(t, files, StaticInstructionsPath).Data); got != "# git\n\n# extra\n" {
		t.Fatalf("instructions = %q", got)
	}
	if fileByPath(t, files, StaticMCPPath).Mode != 0o400 || fileByPath(t, files, StaticInstructionsPath).Mode != 0o400 {
		t.Fatalf("context files should be mode 0400")
	}
}

func TestContextFilesIncludeConfiguredMCPServers(t *testing.T) {
	cfg := testTobyConfig(t, []byte(`
mcp:
  docs:
    type: local
    command: [npx, -y, docs-mcp]
    environment:
      TOKEN: abc
    tools: [search]
  remote:
    type: remote
    url: https://example.com/mcp
    headers:
      X-Token: abc
  off:
    type: local
    command: off
    enabled: false
`))
	files, err := renderContextFiles(t, nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	data := string(fileByPath(t, files, StaticMCPPath).Data)
	for _, want := range []string{`"docs"`, `"command": "npx"`, `"-y"`, `"docs-mcp"`, `"TOKEN": "abc"`, `"tools": [`, `"remote"`, `"type": "http"`, `"url": "http://127.0.0.1:12345/proxy/`} {
		if !strings.Contains(data, want) {
			t.Fatalf("config missing %q:\n%s", want, data)
		}
	}
	if strings.Contains(data, `"off"`) {
		t.Fatalf("disabled server rendered:\n%s", data)
	}
}

func renderContextFiles(t *testing.T, instructions [][]byte, cfg *tobyconfig.Service) ([]contextfiles.File, error) {
	t.Helper()
	var service *contextfiles.Service
	app := fxtest.New(t,
		fx.Provide(contextfiles.NewService),
		fx.Populate(&service),
	)
	app.RequireStart()
	t.Cleanup(app.RequireStop)
	builder := service.NewBuilder()
	if err := RegisterContextFiles(builder, instructions, cfg, "127.0.0.1:12345", testTobyMCPURL, httpproxy.NewService(httpproxy.ServiceParams{})); err != nil {
		return nil, err
	}
	return builder.Files(), nil
}

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

func decodeMCP(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
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
