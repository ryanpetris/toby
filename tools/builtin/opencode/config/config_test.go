package config

import (
	"encoding/json"
	"testing"

	"petris.dev/toby/config/session"
	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/tools/fake"
)

const (
	testTobyMCPURL  = "http://127.0.0.1:12345/proxy/toby"
	testInstruction = "/run/user/1000/toby/context/user-instructions.md"
)

func renderConfig(t *testing.T, cfg sessionconfig.Config) map[string]any {
	t.Helper()
	service := contextfiles.NewService()
	recorder := fake.NewSandbox("")
	service.SetSandbox(recorder)
	if err := RegisterContextFiles(service.Registrar(t.Context()), cfg); err != nil {
		t.Fatal(err)
	}
	for _, file := range recorder.Files {
		if file.Path == StaticConfigPath {
			var config map[string]any
			if err := json.Unmarshal(file.Data, &config); err != nil {
				t.Fatal(err)
			}
			return config
		}
	}
	t.Fatalf("static file %q not found in %#v", StaticConfigPath, recorder.Files)
	return nil
}

func TestRendersTobyMCPAndInstructions(t *testing.T) {
	config := renderConfig(t, sessionconfig.Config{
		MCPServers:   []sessionconfig.MCPServer{{Name: "toby", URL: testTobyMCPURL}},
		Instructions: sessionconfig.Instructions{Paths: []string{testInstruction}},
	})
	toby := config["mcp"].(map[string]any)["toby"].(map[string]any)
	if toby["type"] != "remote" || toby["url"] != testTobyMCPURL || toby["enabled"] != true {
		t.Fatalf("mcp.toby = %#v", toby)
	}
	instructions := config["instructions"].([]any)
	if len(instructions) != 1 || instructions[0] != testInstruction {
		t.Fatalf("instructions = %#v", instructions)
	}
}

func TestRendersMCPServersAsRemote(t *testing.T) {
	config := renderConfig(t, sessionconfig.Config{
		MCPServers: []sessionconfig.MCPServer{
			{Name: "docs", URL: "http://127.0.0.1:12345/proxy/docs"},
			{Name: "toby", URL: testTobyMCPURL},
		},
	})
	docs := config["mcp"].(map[string]any)["docs"].(map[string]any)
	if docs["type"] != "remote" || docs["url"] != "http://127.0.0.1:12345/proxy/docs" || docs["enabled"] != true {
		t.Fatalf("mcp.docs = %#v", docs)
	}
}

func TestRendersPermissionPaths(t *testing.T) {
	config := renderConfig(t, sessionconfig.Config{
		Permissions: map[string]string{"/tmp": "deny", "/custom": "allow"},
	})
	external := config["permission"].(map[string]any)["external_directory"].(map[string]any)
	if external["/tmp"] != "deny" || external["/custom"] != "allow" {
		t.Fatalf("external_directory = %#v", external)
	}
}

func TestRendersOpenAIProvider(t *testing.T) {
	config := renderConfig(t, sessionconfig.Config{
		Providers: []sessionconfig.Provider{{
			ID:      "local",
			Type:    "openai",
			Name:    "Local",
			BaseURL: "http://127.0.0.1:12345/proxy/abc",
			Models:  map[string]any{"alpha": map[string]any{"name": "alpha"}},
		}},
	})
	provider := config["provider"].(map[string]any)["local"].(map[string]any)
	if provider["npm"] != "@ai-sdk/openai-compatible" {
		t.Fatalf("npm = %#v", provider["npm"])
	}
	if provider["name"] != "Local" {
		t.Fatalf("name = %#v", provider["name"])
	}
	if baseURL := provider["options"].(map[string]any)["baseURL"]; baseURL != "http://127.0.0.1:12345/proxy/abc" {
		t.Fatalf("baseURL = %#v", baseURL)
	}
	if _, ok := provider["models"].(map[string]any)["alpha"]; !ok {
		t.Fatalf("models = %#v", provider["models"])
	}
}

func TestRendersAnthropicProvider(t *testing.T) {
	config := renderConfig(t, sessionconfig.Config{
		Providers: []sessionconfig.Provider{{
			ID:      "anthropic",
			Type:    "anthropic",
			BaseURL: "http://127.0.0.1:12345/proxy/xyz",
			Models:  map[string]any{"claude": map[string]any{"name": "Claude"}},
		}},
	})
	provider := config["provider"].(map[string]any)["anthropic"].(map[string]any)
	if provider["npm"] != "@ai-sdk/anthropic" {
		t.Fatalf("npm = %#v", provider["npm"])
	}
	if name := provider["models"].(map[string]any)["claude"].(map[string]any)["name"]; name != "Claude" {
		t.Fatalf("claude model name = %#v", name)
	}
}

func TestGitignoreWritten(t *testing.T) {
	service := contextfiles.NewService()
	recorder := fake.NewSandbox("")
	service.SetSandbox(recorder)
	if err := RegisterContextFiles(service.Registrar(t.Context()), sessionconfig.Config{}); err != nil {
		t.Fatal(err)
	}
	var found *contextfiles.File
	for i := range recorder.Files {
		if recorder.Files[i].Path == StaticGitignorePath {
			found = &recorder.Files[i]
		}
	}
	if found == nil || string(found.Data) != "*\n" {
		t.Fatalf("gitignore = %#v", found)
	}
}
