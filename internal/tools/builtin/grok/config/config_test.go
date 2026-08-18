package config

// Tests Grok's native generated files.

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/pelletier/go-toml/v2"

	sessionconfig "petris.dev/toby/internal/config/session"
	"petris.dev/toby/internal/configpatch"
	"petris.dev/toby/internal/toolfiles"
)

func TestNativeFilesUsePluginMCPAndExactPaths(t *testing.T) {
	files, err := NativeFiles("grok", toolfiles.Ownership{UID: 1000, GID: 1001}, sessionconfig.Config{
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
		Instructions: sessionconfig.Instructions{
			Contents: [][]byte{[]byte("# native instructions\n")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	manifest := nativeFileByTarget(t, files, NativePluginManifestPath)
	if manifest.Mode != 0o644 || manifest.UID != 1000 || manifest.GID != 1001 {
		t.Fatalf("plugin manifest metadata = %#v", manifest)
	}
	decodedManifest := decodeJSON(t, manifest.Data)
	if decodedManifest["name"] != PluginName {
		t.Fatalf("plugin name = %#v", decodedManifest["name"])
	}

	mcpFile := nativeFileByTarget(t, files, NativePluginMCPPath)
	if mcpFile.Mode != 0o600 || mcpFile.UID != 1000 || mcpFile.GID != 1001 {
		t.Fatalf("plugin MCP metadata = %#v", mcpFile)
	}
	servers := decodeJSON(t, mcpFile.Data)["mcpServers"].(map[string]any)
	local := servers["local"].(map[string]any)
	args := local["args"].([]any)
	if local["command"] != "/toby/bin/tobys" ||
		local["enabled"] != true ||
		len(args) != 4 ||
		args[2] != "--" ||
		args[3] != "local" {
		t.Fatalf("local MCP = %#v", local)
	}
	remote := servers["remote"].(map[string]any)
	if remote["url"] != "http://127.0.0.1:8080/mcp" || remote["enabled"] != true {
		t.Fatalf("remote MCP = %#v", remote)
	}

	configFile := nativeFileByTarget(t, files, NativeConfigPath)
	if configFile.Mode != 0o600 || len(configFile.Data) != 0 {
		t.Fatalf("config patch metadata = %#v", configFile)
	}
	wantPatch := configpatch.Patch{
		Ensure: []configpatch.Value{{
			Path:  "/plugins/enabled",
			Value: PluginName,
		}},
	}
	if !reflect.DeepEqual(configFile.Patch, wantPatch) {
		t.Fatalf("config patch = %#v, want %#v", configFile.Patch, wantPatch)
	}

	patched, err := configpatch.ApplyTOML([]byte(`
[ui]
max_thoughts_width = 120

[plugins]
enabled = ["already"]
`), configFile.Patch)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Plugins struct {
			Enabled []string `toml:"enabled"`
		} `toml:"plugins"`
		UI struct {
			MaxThoughtsWidth int `toml:"max_thoughts_width"`
		} `toml:"ui"`
		MCPServers map[string]any `toml:"mcp_servers"`
	}
	if err := toml.Unmarshal(patched, &config); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(config.Plugins.Enabled, []string{"already", PluginName}) {
		t.Fatalf("enabled plugins = %#v", config.Plugins.Enabled)
	}
	if config.UI.MaxThoughtsWidth != 120 {
		t.Fatalf("max_thoughts_width = %d", config.UI.MaxThoughtsWidth)
	}
	if len(config.MCPServers) != 0 {
		t.Fatalf("config still contains MCP servers: %#v", config.MCPServers)
	}

	instructionFile := nativeFileByTarget(t, files, NativeInstructionsPath)
	if instructionFile.Mode != 0o644 || string(instructionFile.Data) != "# native instructions\n" {
		t.Fatalf("native instructions = %#v", instructionFile)
	}
}

func TestNativeFilesReplacePluginMCPWhenSessionOmitsAServer(t *testing.T) {
	first, err := NativeFiles("grok", toolfiles.Ownership{}, sessionconfig.Config{
		MCPServers: []sessionconfig.MCPServer{
			{
				Name:      "keep",
				Transport: sessionconfig.MCPTransportStdio,
				Command:   "/toby/bin/tobys",
				Args:      []string{"resource", "connect", "--", "keep"},
			},
			{
				Name:      "drop",
				Transport: sessionconfig.MCPTransportHTTP,
				URL:       "http://127.0.0.1:8080/mcp/drop",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := decodeJSON(t, nativeFileByTarget(t, first, NativePluginMCPPath).Data)["mcpServers"].(map[string]any)["drop"]; !ok {
		t.Fatal("first render missing drop server")
	}

	second, err := NativeFiles("grok", toolfiles.Ownership{}, sessionconfig.Config{
		MCPServers: []sessionconfig.MCPServer{
			{
				Name:      "keep",
				Transport: sessionconfig.MCPTransportStdio,
				Command:   "/toby/bin/tobys",
				Args:      []string{"resource", "connect", "--", "keep"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	servers := decodeJSON(t, nativeFileByTarget(t, second, NativePluginMCPPath).Data)["mcpServers"].(map[string]any)
	if _, ok := servers["drop"]; ok {
		t.Fatalf("second render still has drop server: %#v", servers)
	}
	if _, ok := servers["keep"]; !ok {
		t.Fatalf("second render missing keep server: %#v", servers)
	}
}

func decodeJSON(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	return value
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
