package config

// Tests Claude's native generated files.

import (
	"encoding/json"
	"testing"

	"petris.dev/toby/internal/config/session"
	"petris.dev/toby/internal/toolfiles"
)

func decode(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	return value
}

func TestNativeFilesUseExactPathsAndMCPTransports(t *testing.T) {
	files, err := NativeFiles("claude", toolfiles.Ownership{UID: 1000, GID: 1001}, sessionconfig.Config{
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

	mcpFile := nativeFileByTarget(t, files, NativeMCPPath)
	if mcpFile.Mode != 0o600 || mcpFile.UID != 1000 || mcpFile.GID != 1001 {
		t.Fatalf("native MCP metadata = %#v", mcpFile)
	}
	servers := decode(t, mcpFile.Data)["mcpServers"].(map[string]any)
	local := servers["local"].(map[string]any)
	args := local["args"].([]any)
	if local["type"] != "stdio" ||
		local["command"] != "/toby/bin/tobys" ||
		len(args) != 4 ||
		args[2] != "--" ||
		args[3] != "local" {
		t.Fatalf("local MCP = %#v", local)
	}
	remote := servers["remote"].(map[string]any)
	if remote["type"] != "http" || remote["url"] != "http://127.0.0.1:8080/mcp" {
		t.Fatalf("remote MCP = %#v", remote)
	}

	settingsFile := nativeFileByTarget(t, files, NativeSettingsPath)
	if settingsFile.Mode != 0o600 {
		t.Fatalf("native settings metadata = %#v", settingsFile)
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
