package config

// Tests Codex's current CLI overrides and native generated files.

import (
	"slices"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"petris.dev/toby/internal/config/session"
	"petris.dev/toby/internal/toolfiles"
)

const testTobyMCPURL = "http://127.0.0.1:12345/mcp/toby"

func TestMCPConfigArgsIncludeTobyMCP(t *testing.T) {
	args, err := MCPConfigArgs(sessionconfig.Config{
		MCPServers: []sessionconfig.MCPServer{{
			Name:      "toby",
			Transport: sessionconfig.MCPTransportHTTP,
			URL:       testTobyMCPURL,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	servers := parseMCPConfigArgs(t, args)
	if len(servers) != 1 {
		t.Fatalf("MCP servers = %#v", servers)
	}
	toby := servers["toby"]
	if toby.URL != testTobyMCPURL || !toby.Enabled {
		t.Fatalf("Toby MCP = %#v", toby)
	}
}

func TestMCPConfigArgsPreserveLiteralMCPServerNames(t *testing.T) {
	args, err := MCPConfigArgs(sessionconfig.Config{
		MCPServers: []sessionconfig.MCPServer{
			{Name: "docs", Transport: sessionconfig.MCPTransportHTTP, URL: "http://127.0.0.1:12345/mcp/docs"},
			{
				Name:      "docs.api",
				Transport: sessionconfig.MCPTransportStdio,
				Command:   "/toby/bin/tobys",
				Args:      []string{"resource", "connect", "--", "docs.api"},
			},
			{
				Name:      "local:search",
				Transport: sessionconfig.MCPTransportHTTP,
				URL:       "http://127.0.0.1:12345/mcp/local:search",
			},
			{Name: "toby", Transport: sessionconfig.MCPTransportHTTP, URL: testTobyMCPURL},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	servers := parseMCPConfigArgs(t, args)
	if len(servers) != 4 {
		t.Fatalf("MCP servers = %#v", servers)
	}
	docs := servers["docs"]
	if docs.URL != "http://127.0.0.1:12345/mcp/docs" || !docs.Enabled {
		t.Fatalf("docs MCP = %#v", docs)
	}
	docsAPI := servers["docs.api"]
	if docsAPI.Command != "/toby/bin/tobys" ||
		!slices.Equal(docsAPI.Args, []string{"resource", "connect", "--", "docs.api"}) ||
		!docsAPI.Enabled {
		t.Fatalf("docs.api MCP = %#v", docsAPI)
	}
	localSearch := servers["local:search"]
	if localSearch.URL != "http://127.0.0.1:12345/mcp/local:search" ||
		!localSearch.Enabled {
		t.Fatalf("local:search MCP = %#v", localSearch)
	}
}

func TestMCPConfigArgsExcludeFileBasedInstructions(t *testing.T) {
	args, err := MCPConfigArgs(sessionconfig.Config{
		MCPServers: []sessionconfig.MCPServer{{
			Name:      "toby",
			Transport: sessionconfig.MCPTransportHTTP,
			URL:       testTobyMCPURL,
		}},
		Instructions: sessionconfig.Instructions{
			Contents: [][]byte{[]byte("# native instructions\n")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	toby := parseMCPConfigArgs(t, args)["toby"]
	if toby.URL != testTobyMCPURL || !toby.Enabled {
		t.Fatalf("Toby MCP = %#v", toby)
	}
	for _, arg := range args {
		if strings.Contains(arg, "developer_instructions") {
			t.Fatalf("MCP-only args contain instructions: %#v", args)
		}
	}
}

type parsedMCPServer struct {
	Command string   `toml:"command"`
	Args    []string `toml:"args"`
	URL     string   `toml:"url"`
	Enabled bool     `toml:"enabled"`
}

func parseMCPConfigArgs(t *testing.T, args []string) map[string]parsedMCPServer {
	t.Helper()
	if len(args) != 2 || args[0] != "-c" {
		t.Fatalf("MCP config args = %#v", args)
	}

	var config struct {
		MCPServers map[string]parsedMCPServer `toml:"mcp_servers"`
	}
	if err := toml.Unmarshal([]byte(args[1]), &config); err != nil {
		t.Fatalf("parse MCP config override %q: %v", args[1], err)
	}

	return config.MCPServers
}

func TestNativeFilesUseExactPathsAndMCPTransports(t *testing.T) {
	files, err := NativeFiles("codex", toolfiles.Ownership{UID: 1000, GID: 1001}, sessionconfig.Config{
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
		Projects: []string{
			"/toby/workspace/alpha",
			"/toby/workspace/beta",
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
		MCPServers map[string]parsedMCPServer `toml:"mcp_servers"`
		Projects   map[string]struct {
			TrustLevel string `toml:"trust_level"`
		} `toml:"projects"`
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
	if len(config.Projects) != 2 {
		t.Fatalf("trusted projects = %#v", config.Projects)
	}
	for _, project := range []string{
		"/toby/workspace/alpha",
		"/toby/workspace/beta",
	} {
		if config.Projects[project].TrustLevel != "trusted" {
			t.Fatalf(
				"trusted project %q = %#v",
				project,
				config.Projects[project],
			)
		}
		table := `[projects.'` + project + `']`
		if !strings.Contains(string(configFile.Data), table) {
			t.Fatalf(
				"native config does not contain %q:\n%s",
				table,
				configFile.Data,
			)
		}
	}
	if _, broadTrust := config.Projects["/toby"]; broadTrust {
		t.Fatalf("native config trusts broad Toby root: %#v", config.Projects)
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
