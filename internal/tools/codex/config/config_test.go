package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"petris.dev/toby/internal/config/toby"
	"petris.dev/toby/internal/control/httpproxy"
)

const testTobyMCPURL = "http://127.0.0.1:12345/proxy/toby"

func TestConfigArgsIncludeTobyMCPAndInstructions(t *testing.T) {
	args, err := ConfigArgs([][]byte{[]byte("# git\n"), []byte("# extra\n")}, nil, "", testTobyMCPURL, nil)
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

func TestConfigArgsIncludeConfiguredHTTPMCPProxies(t *testing.T) {
	cfg := testTobyConfig(t, []byte(`
mcps:
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
	args, err := ConfigArgs(nil, cfg, "127.0.0.1:12345", testTobyMCPURL, httpproxy.NewService(httpproxy.ServiceParams{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`mcp_servers.local.command='local-mcp'`,
		`mcp_servers.local.enabled=true`,
		`mcp_servers.docs.enabled=true`,
	} {
		if !containsConfigOverride(args, want) {
			t.Fatalf("args missing %q: %#v", want, args)
		}
	}
	if !containsConfigOverridePrefix(args, `mcp_servers.docs.url='http://127.0.0.1:12345/proxy/`) {
		t.Fatalf("args missing proxied docs URL: %#v", args)
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

func containsConfigOverridePrefix(args []string, prefix string) bool {
	for i, arg := range args {
		if arg == "-c" && i+1 < len(args) && strings.HasPrefix(args[i+1], prefix) {
			return true
		}
		if strings.HasPrefix(arg, prefix) {
			return true
		}
	}
	return false
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
