package config

import (
	"slices"
	"testing"

	"petris.dev/toby/config/session"
)

const testTobyMCPURL = "http://127.0.0.1:12345/proxy/toby"

func TestConfigArgsIncludeTobyMCPAndInstructions(t *testing.T) {
	args, err := ConfigArgs(sessionconfig.Config{
		MCPServers:   []sessionconfig.MCPServer{{Name: "toby", URL: testTobyMCPURL}},
		Instructions: sessionconfig.Instructions{Contents: [][]byte{[]byte("# git\n"), []byte("# extra\n")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`mcp_servers.toby.url='http://127.0.0.1:12345/proxy/toby'`,
		`mcp_servers.toby.enabled=true`,
		`developer_instructions="# git\n\n# extra\n"`,
	} {
		if !containsConfigOverride(args, want) {
			t.Fatalf("args missing %q: %#v", want, args)
		}
	}
}

func TestConfigArgsRenderMCPServers(t *testing.T) {
	args, err := ConfigArgs(sessionconfig.Config{
		MCPServers: []sessionconfig.MCPServer{
			{Name: "docs", URL: "http://127.0.0.1:12345/proxy/docs"},
			{Name: "local", URL: "http://127.0.0.1:12345/proxy/local"},
			{Name: "toby", URL: testTobyMCPURL},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`mcp_servers.docs.enabled=true`,
		`mcp_servers.local.enabled=true`,
		`mcp_servers.docs.url='http://127.0.0.1:12345/proxy/docs'`,
		`mcp_servers.local.url='http://127.0.0.1:12345/proxy/local'`,
	} {
		if !containsConfigOverride(args, want) {
			t.Fatalf("args missing %q: %#v", want, args)
		}
	}
}

func containsConfigOverride(args []string, override string) bool {
	for i, arg := range args {
		if arg == "-c" && i+1 < len(args) && args[i+1] == override {
			return true
		}
	}
	return slices.Contains(args, override)
}
