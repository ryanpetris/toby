package config

// Tests for Deep Agents Code synthetic MCP and instruction files.

import (
	"encoding/json"
	"testing"

	"petris.dev/toby/internal/config/session"
	"petris.dev/toby/internal/toolfiles"
)

func decodeMCP(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestNativeFilesUseExactPathsAndMCPTransports(t *testing.T) {
	files, err := NativeFiles("dcode", toolfiles.Ownership{UID: 1000, GID: 1001}, sessionconfig.Config{
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
	servers := decodeMCP(t, mcpFile.Data)["mcpServers"].(map[string]any)
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
