package config

import (
	"strings"
	"testing"

	"petris.dev/toby/config/session"
	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/tools/fake"
)

func render(t *testing.T, cfg sessionconfig.Config) []contextfiles.File {
	t.Helper()
	service := contextfiles.NewService()
	recorder := fake.NewSandbox("")
	service.SetSandbox(recorder)
	if err := RegisterContextFiles(service.Registrar(t.Context()), cfg); err != nil {
		t.Fatal(err)
	}
	return recorder.Files
}

func TestContextFilesIncludeTobyConfig(t *testing.T) {
	files := render(t, sessionconfig.Config{
		MCPServers: []sessionconfig.MCPServer{{Name: "toby", URL: "http://127.0.0.1:12345/proxy/toby"}},
	})
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

func TestContextFilesRenderMCPServers(t *testing.T) {
	files := render(t, sessionconfig.Config{
		MCPServers: []sessionconfig.MCPServer{
			{Name: "docs", URL: "http://127.0.0.1:12345/proxy/docs"},
			{Name: "remote", URL: "http://127.0.0.1:12345/proxy/remote"},
			{Name: "toby", URL: "http://127.0.0.1:12345/proxy/toby"},
		},
	})
	config := string(fileByPath(t, files, StaticConfigPath).Data)
	for _, want := range []string{`[mcp_servers.docs]`, `url = 'http://127.0.0.1:12345/proxy/docs'`, `[mcp_servers.remote]`, `url = 'http://127.0.0.1:12345/proxy/remote'`} {
		if !strings.Contains(config, want) {
			t.Fatalf("config missing %q:\n%s", want, config)
		}
	}
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
