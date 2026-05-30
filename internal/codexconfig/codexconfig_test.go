package codexconfig

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"petris.dev/toby/internal/tobyconfig"
)

func TestConfigArgsIncludeTobyMCPAndInstructions(t *testing.T) {
	args, err := ConfigArgs([][]byte{[]byte("# git\n"), []byte("# extra\n")}, nil)
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

func TestConfigArgsIncludeConfiguredHTTPMCPProxies(t *testing.T) {
	cfg := testTobyConfig(t, []byte(`
mcp:
  docs:
    type: remote
    url: https://example.com/mcp
    headers:
      Authorization: Bearer secret
  local:
    type: local
    command: local-mcp
  off:
    type: remote
    url: https://off.example.com/mcp
    enabled: false
`))
	args, err := ConfigArgs(nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`mcp_servers.docs.command='toby'`,
		`mcp_servers.docs.args=['sandbox', 'mcp', 'docs']`,
		`mcp_servers.docs.enabled=true`,
		`mcp_servers.docs.env_vars=['TOBY_CONTROL_URL', 'TOBY_CONTROL_TOKEN']`,
	} {
		if !containsConfigOverride(args, want) {
			t.Fatalf("args missing %q: %#v", want, args)
		}
	}
	for _, unwanted := range []string{"example.com", "Bearer secret", "mcp_servers.local", "mcp_servers.off"} {
		for _, arg := range args {
			if strings.Contains(arg, unwanted) {
				t.Fatalf("args leaked %q: %#v", unwanted, args)
			}
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

func testTobyConfig(t *testing.T, data []byte) *tobyconfig.Service {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := tobyconfig.Load(dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
