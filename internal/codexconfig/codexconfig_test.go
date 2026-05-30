package codexconfig

import (
	"slices"
	"testing"
)

func TestConfigArgsIncludeTobyMCPAndInstructions(t *testing.T) {
	args, err := ConfigArgs([][]byte{[]byte("# git\n"), []byte("# extra\n")})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`mcp_servers.toby.command='toby'`,
		`mcp_servers.toby.args=['sandbox', 'mcp']`,
		`mcp_servers.toby.enabled=true`,
		`mcp_servers.toby.env_vars=['TOBY_CONTROL_URL', 'TOBY_CONTROL_TOKEN']`,
		`developer_instructions="# git\n\n# extra\n"`,
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
