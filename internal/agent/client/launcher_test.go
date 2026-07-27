package client

// Verifies detached agent autostart inherits runtime necessities without
// retaining the launch shell's credentials or agent authority.

import (
	"slices"
	"testing"
)

func TestAgentServiceEnvironmentIsAllowlisted(t *testing.T) {
	environment := agentEnvironment([]string{
		"HOME=/home/test",
		"XDG_RUNTIME_DIR=/run/user/1000",
		"PATH=/usr/bin",
		"LC_MESSAGES=C",
		"HTTPS_PROXY=http://proxy.example",
		"SSH_AUTH_SOCK=/run/user/1000/agent",
		"GPG_AGENT_INFO=/run/user/1000/gpg",
		"API_TOKEN=secret",
		"MCP_URL=https://secret.example/mcp",
		"HOME=/replacement",
		"malformed",
	})

	want := []string{
		"XDG_RUNTIME_DIR=/run/user/1000",
		"PATH=/usr/bin",
		"LC_MESSAGES=C",
		"HTTPS_PROXY=http://proxy.example",
		"HOME=/replacement",
	}
	if !slices.Equal(environment, want) {
		t.Fatalf("agent environment = %#v, want %#v", environment, want)
	}
}
