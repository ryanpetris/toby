package cli

// Verifies config-free informational invocation classification.

import "testing"

func TestIsConfigFreeInvocation(t *testing.T) {
	tests := []struct {
		name      string
		arguments []string
		want      bool
	}{
		{name: "no arguments", want: true},
		{name: "root help", arguments: []string{"--help"}, want: true},
		{name: "short help", arguments: []string{"-h"}, want: true},
		{name: "subcommand help", arguments: []string{"opencode", "project", "--help"}, want: true},
		{name: "numeric subcommand help", arguments: []string{"opencode", "project", "--help=1"}, want: true},
		{name: "uppercase subcommand help", arguments: []string{"opencode", "project", "--help=TRUE"}, want: true},
		{name: "short boolean subcommand help", arguments: []string{"opencode", "project", "-h=t"}, want: true},
		{name: "disabled subcommand help", arguments: []string{"opencode", "project", "--help=false"}},
		{name: "help command", arguments: []string{"help", "opencode"}, want: true},
		{name: "help command after flag", arguments: []string{"--debug", "help", "opencode"}, want: true},
		{name: "version", arguments: []string{"--version"}, want: true},
		{name: "numeric version", arguments: []string{"opencode", "project", "--version=1"}, want: true},
		{name: "agent status", arguments: []string{"agent", "status"}, want: true},
		{name: "agent stop", arguments: []string{"agent", "stop"}, want: true},
		{name: "agent resources", arguments: []string{"agent", "resources"}, want: true},
		{name: "agent logs", arguments: []string{"agent", "logs", "resource"}, want: true},
		{name: "volume", arguments: []string{"volume"}, want: true},
		{name: "volume list", arguments: []string{"volume", "list"}, want: true},
		{name: "volume list alias", arguments: []string{"volume", "ls"}, want: true},
		{name: "volume filtered list", arguments: []string{"volume", "list", "--type", "tool", "--profile", "work"}, want: true},
		{name: "volume create", arguments: []string{"volume", "create", "--type", "home", "--name", "workspace"}, want: true},
		{name: "volume inspect", arguments: []string{"volume", "inspect", "0123456789ab"}, want: true},
		{name: "volume inspect metadata", arguments: []string{"volume", "inspect", "--type", "tool", "--name", "opencode", "--purpose", "data"}, want: true},
		{name: "volume inspect JSON", arguments: []string{"volume", "inspect", "0123456789ab", "--output", "json"}, want: true},
		{name: "volume path", arguments: []string{"volume", "path", "0123456789ab"}, want: true},
		{name: "volume metadata path", arguments: []string{"volume", "path", "--type", "home", "--name", "workspace"}, want: true},
		{name: "volume remove", arguments: []string{"volume", "remove", "0123456789ab"}, want: true},
		{name: "volume remove alias", arguments: []string{"volume", "rm", "0123456789ab"}, want: true},
		{name: "volume filtered remove", arguments: []string{"volume", "remove", "--force", "--type", "tool"}, want: true},
		{name: "image", arguments: []string{"image"}, want: true},
		{name: "image pull", arguments: []string{"image", "pull", "alpine"}, want: true},
		{name: "image build", arguments: []string{"image", "build", ".", "--output", "image.tar"}, want: true},
		{name: "image import", arguments: []string{"image", "import", "image.tar", "example:latest"}, want: true},
		{name: "image list", arguments: []string{"image", "list", "--dangling"}, want: true},
		{name: "image list alias", arguments: []string{"image", "ls"}, want: true},
		{name: "image inspect", arguments: []string{"image", "inspect", "alpine", "--output", "json"}, want: true},
		{name: "image path", arguments: []string{"image", "path", "alpine"}, want: true},
		{name: "image remove", arguments: []string{"image", "remove", "alpine", "--force"}, want: true},
		{name: "image remove alias", arguments: []string{"image", "rm", "alpine", "--force"}, want: true},
		{name: "image filtered remove", arguments: []string{"image", "remove", "--reference", "alpine", "--force"}, want: true},
		{name: "image prune", arguments: []string{"image", "prune", "--force"}, want: true},
		{name: "launch", arguments: []string{"opencode", "project"}},
		{name: "help project", arguments: []string{"opencode", "help"}},
		{name: "configured launch", arguments: []string{"--config", "launch.yaml"}},
		{
			name:      "config value named help",
			arguments: []string{"--config", "--help"},
		},
		{
			name:      "payload help",
			arguments: []string{"exec", "project", "--", "--help"},
		},
		{
			name:      "conflicting launch output modes",
			arguments: []string{"codex", "project", "--debug", "--quiet"},
			want:      true,
		},
		{
			name:      "conflicting configured output modes",
			arguments: []string{"--config", "launch.yaml", "--debug=1", "--quiet=TRUE"},
			want:      true,
		},
		{
			name:      "last debug value disables conflict",
			arguments: []string{"codex", "project", "--debug", "--debug=false", "--quiet"},
		},
		{
			name:      "payload output modes",
			arguments: []string{"exec", "project", "--", "--debug", "--quiet"},
		},
		{
			name:      "output mode used as flag value",
			arguments: []string{"codex", "project", "--project", "--debug", "--quiet"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsConfigFreeInvocation(test.arguments); got != test.want {
				t.Fatalf(
					"IsConfigFreeInvocation(%q) = %v, want %v",
					test.arguments,
					got,
					test.want,
				)
			}
		})
	}
}

func TestInformationalFlagEnabledAcceptsPflagTrueSpellings(t *testing.T) {
	for _, name := range []string{"--help=", "-h=", "--version="} {
		for _, value := range []string{"1", "t", "T", "TRUE", "true", "True"} {
			argument := name + value
			if !informationalFlagEnabled(argument) {
				t.Errorf("%q was not recognized as enabled", argument)
			}
		}
		for _, value := range []string{"0", "f", "F", "FALSE", "false", "False"} {
			argument := name + value
			if informationalFlagEnabled(argument) {
				t.Errorf("%q was recognized as enabled", argument)
			}
		}
	}
}
