package config

import (
	"encoding/json"
	"strings"
	"testing"

	"petris.dev/toby/config/session"
	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/tools/fake"
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

func TestContextFilesIncludesTobyMCPServer(t *testing.T) {
	files := render(t, sessionconfig.Config{
		MCPServers: []sessionconfig.MCPServer{{Name: "toby", URL: testTobyMCPURL}},
	})
	mcp := decode(t, fileByPath(t, files, StaticMcpPath).Data)
	toby := mcp["mcpServers"].(map[string]any)["toby"].(map[string]any)
	if toby["type"] != "http" || toby["url"] != testTobyMCPURL {
		t.Fatalf("mcp.toby = %#v", toby)
	}
}

func TestContextFilesRendersMCPServersAsHTTP(t *testing.T) {
	files := render(t, sessionconfig.Config{
		MCPServers: []sessionconfig.MCPServer{
			{Name: "docs", URL: "http://127.0.0.1:12345/proxy/docs"},
			{Name: "remote", URL: "http://127.0.0.1:12345/proxy/remote"},
			{Name: "toby", URL: testTobyMCPURL},
		},
	})
	servers := decode(t, fileByPath(t, files, StaticMcpPath).Data)["mcpServers"].(map[string]any)
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

func TestContextFilesIncludesPermissionDirectories(t *testing.T) {
	projectRoot := "/toby/workspace"
	files := render(t, sessionconfig.Config{
		Permissions: map[string]string{
			projectRoot:          "allow",
			"/tmp":               "allow",
			testHome + "/go":     "allow",
			testHome + "/.cache": "allow",
			"/globbed/**":        "allow",
		},
	})
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
		if strings.ContainsAny(dir.(string), "*?[") {
			t.Fatalf("additionalDirectories should not contain glob patterns: %q", dir)
		}
	}
}

func TestContextFilesPermissionDenyDropped(t *testing.T) {
	files := render(t, sessionconfig.Config{
		Permissions: map[string]string{"/tmp": "deny", "/custom": "allow"},
	})
	settings := decode(t, fileByPath(t, files, StaticSettingsPath).Data)
	dirs := settings["permissions"].(map[string]any)["additionalDirectories"].([]any)
	got := map[string]bool{}
	for _, dir := range dirs {
		got[dir.(string)] = true
	}
	if got["/tmp"] {
		t.Fatalf("deny should drop /tmp from additionalDirectories: %#v", dirs)
	}
	if !got["/custom"] {
		t.Fatalf("allow /custom missing: %#v", dirs)
	}
}

func TestContextFilesPassesDirectoriesVerbatim(t *testing.T) {
	files := render(t, sessionconfig.Config{
		Permissions: map[string]string{
			"/foobar/":    "allow",
			"/foobar":     "allow",
			"/":           "allow",
			"/globbed/**": "allow",
		},
	})
	settings := decode(t, fileByPath(t, files, StaticSettingsPath).Data)
	dirs := settings["permissions"].(map[string]any)["additionalDirectories"].([]any)
	got := map[string]bool{}
	for _, dir := range dirs {
		got[dir.(string)] = true
	}
	// Paths are listed exactly as written — the trailing slash is not stripped.
	for _, want := range []string{"/foobar/", "/foobar", "/"} {
		if !got[want] {
			t.Fatalf("additionalDirectories missing %q: %#v", want, dirs)
		}
	}
	// Glob patterns are not valid additionalDirectories and are dropped.
	if got["/globbed/**"] {
		t.Fatalf("glob pattern should be dropped: %#v", dirs)
	}
}

func TestContextFilesCombinesInstructions(t *testing.T) {
	files := render(t, sessionconfig.Config{
		Instructions: sessionconfig.Instructions{Contents: [][]byte{[]byte("# git\n"), []byte("# context\n")}},
	})
	got := string(fileByPath(t, files, StaticInstructionsPath).Data)
	if !strings.Contains(got, "# git") || !strings.Contains(got, "# context") {
		t.Fatalf("instructions = %q", got)
	}
}
