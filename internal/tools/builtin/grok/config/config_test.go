package config

// Tests Grok's native generated files.

import (
	"slices"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"petris.dev/toby/internal/config/session"
	"petris.dev/toby/internal/toolfiles"
)

func TestNativeFilesUseExactPathsAndMCPTransports(t *testing.T) {
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

	configFile := nativeFileByTarget(t, files, NativeConfigPath)
	if configFile.Mode != 0o600 || configFile.UID != 1000 || configFile.GID != 1001 {
		t.Fatalf("native config metadata = %#v", configFile)
	}
	var config struct {
		MCPServers map[string]struct {
			Command string   `toml:"command"`
			Args    []string `toml:"args"`
			URL     string   `toml:"url"`
			Enabled bool     `toml:"enabled"`
		} `toml:"mcp_servers"`
	}
	if err := toml.Unmarshal(configFile.Data, &config); err != nil {
		t.Fatal(err)
	}
	local := config.MCPServers["local"]
	if local.Command != "/toby/bin/tobys" ||
		!slices.Equal(local.Args, []string{"resource", "connect", "--", "local"}) ||
		!local.Enabled {
		t.Fatalf("local MCP = %#v", local)
	}
	remote := config.MCPServers["remote"]
	if remote.URL != "http://127.0.0.1:8080/mcp" || !remote.Enabled {
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
