package config

// Tests OpenCode's native generated files.

import (
	"encoding/json"
	"testing"

	"petris.dev/toby/internal/config/session"
	"petris.dev/toby/internal/toolfiles"
)

const (
	testTobyMCPURL = "http://127.0.0.1:12345/mcp/toby"
)

func renderConfig(t *testing.T, cfg sessionconfig.Config) map[string]any {
	t.Helper()
	data, err := NativeConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	return config
}

func TestRendersTobyMCPAndInstructions(t *testing.T) {
	config := renderConfig(t, sessionconfig.Config{
		MCPServers: []sessionconfig.MCPServer{{
			Name:      "toby",
			Transport: sessionconfig.MCPTransportHTTP,
			URL:       testTobyMCPURL,
		}},
	})
	toby := config["mcp"].(map[string]any)["toby"].(map[string]any)
	if toby["type"] != "remote" || toby["url"] != testTobyMCPURL || toby["enabled"] != true {
		t.Fatalf("mcp.toby = %#v", toby)
	}
	instructions := config["instructions"].([]any)
	if len(instructions) != 1 || instructions[0] != NativeInstructionsPath {
		t.Fatalf("instructions = %#v", instructions)
	}
}

func TestRendersMCPServersAsRemote(t *testing.T) {
	config := renderConfig(t, sessionconfig.Config{
		MCPServers: []sessionconfig.MCPServer{
			{Name: "docs", Transport: sessionconfig.MCPTransportHTTP, URL: "http://127.0.0.1:12345/mcp/docs"},
			{Name: "toby", Transport: sessionconfig.MCPTransportHTTP, URL: testTobyMCPURL},
		},
	})
	docs := config["mcp"].(map[string]any)["docs"].(map[string]any)
	if docs["type"] != "remote" || docs["url"] != "http://127.0.0.1:12345/mcp/docs" || docs["enabled"] != true {
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

func TestRendersDirectoryPermissionPaths(t *testing.T) {
	config := renderConfig(t, sessionconfig.Config{
		Permissions: map[string]string{"/foobar": "allow", "/withslash/": "allow", "/": "allow"},
	})
	external := config["permission"].(map[string]any)["external_directory"].(map[string]any)
	// A path without a trailing slash: verbatim plus "<path>/**".
	if external["/foobar"] != "allow" || external["/foobar/**"] != "allow" {
		t.Fatalf("/foobar should expand to itself and /foobar/**: %#v", external)
	}
	// A path with a trailing slash is kept verbatim; the recursive form appends "**".
	if external["/withslash/"] != "allow" || external["/withslash/**"] != "allow" {
		t.Fatalf("/withslash/ should be kept and expand to /withslash/**: %#v", external)
	}
	if _, ok := external["/withslash"]; ok {
		t.Fatalf("trailing slash must not be stripped: %#v", external)
	}
	// Root expands without producing a doubled slash.
	if external["/"] != "allow" || external["/**"] != "allow" {
		t.Fatalf("root should expand to / and /**: %#v", external)
	}
	if _, ok := external["//**"]; ok {
		t.Fatalf("root must not produce //**: %#v", external)
	}
}

func TestRendersOpenAIProvider(t *testing.T) {
	config := renderConfig(t, sessionconfig.Config{
		Models: []sessionconfig.ModelsEndpoint{{
			ID:         "local",
			Type:       "openai",
			Name:       "Local",
			URL:        "http://127.0.0.1:12345/provider/abc",
			Credential: "synthetic-openai",
			Models:     map[string]any{"alpha": map[string]any{"name": "alpha"}},
		}},
	})
	provider := config["provider"].(map[string]any)["local"].(map[string]any)
	if provider["npm"] != "@ai-sdk/openai-compatible" {
		t.Fatalf("npm = %#v", provider["npm"])
	}
	if provider["name"] != "Local" {
		t.Fatalf("name = %#v", provider["name"])
	}
	options := provider["options"].(map[string]any)
	if baseURL := options["baseURL"]; baseURL != "http://127.0.0.1:12345/provider/abc" {
		t.Fatalf("baseURL = %#v", baseURL)
	}
	if apiKey := options["apiKey"]; apiKey != "synthetic-openai" {
		t.Fatalf("apiKey = %#v", apiKey)
	}
	if _, ok := provider["models"].(map[string]any)["alpha"]; !ok {
		t.Fatalf("models = %#v", provider["models"])
	}
}

func TestRendersAnthropicProvider(t *testing.T) {
	config := renderConfig(t, sessionconfig.Config{
		Models: []sessionconfig.ModelsEndpoint{{
			ID:         "anthropic",
			Type:       "anthropic",
			URL:        "http://127.0.0.1:12345/provider/xyz",
			Credential: "synthetic-anthropic",
			Models:     map[string]any{"claude": map[string]any{"name": "Claude"}},
		}},
	})
	provider := config["provider"].(map[string]any)["anthropic"].(map[string]any)
	if provider["npm"] != "@ai-sdk/anthropic" {
		t.Fatalf("npm = %#v", provider["npm"])
	}
	if name := provider["models"].(map[string]any)["claude"].(map[string]any)["name"]; name != "Claude" {
		t.Fatalf("claude model name = %#v", name)
	}
	if apiKey := provider["options"].(map[string]any)["apiKey"]; apiKey != "synthetic-anthropic" {
		t.Fatalf("apiKey = %#v", apiKey)
	}
}

func TestNativeFilesUseExactPathsAndMCPTransports(t *testing.T) {
	files, err := NativeFiles("opencode", toolfiles.Ownership{UID: 1000, GID: 1001}, sessionconfig.Config{
		MCPServers: []sessionconfig.MCPServer{
			{
				Name:      "local",
				Transport: sessionconfig.MCPTransportStdio,
				Command:   "/toby/bin/tobys",
				Args:      []string{"resource", "connect", "--", "local"},
			},
			{
				Name:      "remote",
				Transport: sessionconfig.MCPTransportHTTP,
				URL:       "http://127.0.0.1:8080/mcp",
			},
		},
		Models: []sessionconfig.ModelsEndpoint{
			{
				ID:         "openai",
				Type:       "openai",
				URL:        "http://127.0.0.1:8080/provider/openai",
				Credential: "native-openai",
			},
			{
				ID:         "anthropic",
				Type:       "anthropic",
				URL:        "http://127.0.0.1:8080/provider/anthropic",
				Credential: "native-anthropic",
			},
		},
		Instructions: sessionconfig.Instructions{
			Contents: [][]byte{[]byte("# native instructions\n")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	configFile := nativeFileByTarget(t, files, NativeConfigPath)
	if configFile.Mode != 0o600 || configFile.UID != 1000 || configFile.GID != 1001 {
		t.Fatalf("native config metadata = %#v", configFile)
	}
	var config map[string]any
	if err := json.Unmarshal(configFile.Data, &config); err != nil {
		t.Fatal(err)
	}
	servers := config["mcp"].(map[string]any)
	local := servers["local"].(map[string]any)
	command := local["command"].([]any)
	if local["type"] != "local" ||
		len(command) != 5 ||
		command[0] != "/toby/bin/tobys" ||
		command[3] != "--" ||
		command[4] != "local" {
		t.Fatalf("local MCP = %#v", local)
	}
	remote := servers["remote"].(map[string]any)
	if remote["type"] != "remote" || remote["url"] != "http://127.0.0.1:8080/mcp" {
		t.Fatalf("remote MCP = %#v", remote)
	}
	providers := config["provider"].(map[string]any)
	if got := providers["openai"].(map[string]any)["options"].(map[string]any)["apiKey"]; got != "native-openai" {
		t.Fatalf("OpenAI credential = %#v", got)
	}
	if got := providers["anthropic"].(map[string]any)["options"].(map[string]any)["apiKey"]; got != "native-anthropic" {
		t.Fatalf("Anthropic credential = %#v", got)
	}
	instructions := config["instructions"].([]any)
	if len(instructions) != 1 || instructions[0] != NativeInstructionsPath {
		t.Fatalf("instructions = %#v", instructions)
	}
	priorityFile := nativeFileByTarget(t, files, NativePriorityConfigPath)
	if string(priorityFile.Data) != string(configFile.Data) ||
		priorityFile.Mode != configFile.Mode {
		t.Fatalf(
			"highest-priority native config differs: %#v",
			priorityFile,
		)
	}

	instructionFile := nativeFileByTarget(t, files, NativeInstructionsPath)
	if instructionFile.Mode != 0o644 || string(instructionFile.Data) != "# native instructions\n" {
		t.Fatalf("native instructions = %#v", instructionFile)
	}
}

func nativeFileByTarget(
	t *testing.T,
	files []toolfiles.File,
	target string,
) toolfiles.File {
	t.Helper()
	for _, file := range files {
		if file.Target == target {
			return file
		}
	}
	t.Fatalf("native file %q not found in %#v", target, files)
	return toolfiles.File{}
}
