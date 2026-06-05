package config

import (
	"encoding/json"
	"testing"

	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/sessionconfig"
	"petris.dev/toby/tools/fake"
)

const testTobyMCPURL = "http://127.0.0.1:12345/proxy/toby"

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

func TestContextFilesIncludeTobyMCPAndInstructions(t *testing.T) {
	files := render(t, sessionconfig.Config{
		MCPServers:   []sessionconfig.MCPServer{{Name: "toby", URL: testTobyMCPURL}},
		Instructions: sessionconfig.Instructions{Contents: [][]byte{[]byte("# git\n"), []byte("# extra\n")}},
	})
	mcp := decodeMCP(t, fileByPath(t, files, StaticMCPPath).Data)
	toby := mcp["mcpServers"].(map[string]any)["toby"].(map[string]any)
	if toby["type"] != "http" || toby["url"] != testTobyMCPURL {
		t.Fatalf("toby server = %#v", toby)
	}
	if got := string(fileByPath(t, files, StaticInstructionsPath).Data); got != "# git\n\n# extra\n" {
		t.Fatalf("instructions = %q", got)
	}
	if fileByPath(t, files, StaticMCPPath).Mode != 0o644 || fileByPath(t, files, StaticInstructionsPath).Mode != 0o644 {
		t.Fatalf("context files should be mode 0644")
	}
}

func TestContextFilesRenderMCPServersAsHTTP(t *testing.T) {
	files := render(t, sessionconfig.Config{
		MCPServers: []sessionconfig.MCPServer{
			{Name: "docs", URL: "http://127.0.0.1:12345/proxy/docs"},
			{Name: "remote", URL: "http://127.0.0.1:12345/proxy/remote"},
			{Name: "toby", URL: testTobyMCPURL},
		},
	})
	servers := decodeMCP(t, fileByPath(t, files, StaticMCPPath).Data)["mcpServers"].(map[string]any)
	for name, wantURL := range map[string]string{
		"docs":   "http://127.0.0.1:12345/proxy/docs",
		"remote": "http://127.0.0.1:12345/proxy/remote",
	} {
		entry := servers[name].(map[string]any)
		if entry["type"] != "http" || entry["url"] != wantURL {
			t.Fatalf("mcp.%s = %#v", name, entry)
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

func decodeMCP(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}
