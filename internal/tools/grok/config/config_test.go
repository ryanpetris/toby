package config

import (
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

func TestContextFilesIncludeTobyConfig(t *testing.T) {
	files, err := renderContextFiles(t, [][]byte{[]byte("# git\n"), []byte("# extra\n")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	config := string(fileByPath(t, files, StaticConfigPath).Data)
	for _, want := range []string{`[mcp_servers.toby]`, `url = 'http://127.0.0.1:12345/proxy/toby'`, `enabled = true`} {
		if !strings.Contains(config, want) {
			t.Fatalf("config missing %q:\n%s", want, config)
		}
	}
	if got := Rules([][]byte{[]byte("# git\n"), []byte("# extra\n")}); got != "# git\n\n# extra\n" {
		t.Fatalf("rules = %q", got)
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
    timeout: 3
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
	config := string(fileByPath(t, files, StaticConfigPath).Data)
	for _, want := range []string{`[mcp_servers.docs]`, `command`, `npx`, `-y`, `docs-mcp`, `TOKEN`, `abc`, `startup_timeout_sec = 3`, `[mcp_servers.remote]`, `url = 'http://127.0.0.1:12345/proxy/`} {
		if !strings.Contains(config, want) {
			t.Fatalf("config missing %q:\n%s", want, config)
		}
	}
	if strings.Contains(config, `mcp_servers.off`) {
		t.Fatalf("disabled server rendered:\n%s", config)
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
