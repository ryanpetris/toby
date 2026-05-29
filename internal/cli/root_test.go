package cli

import (
	"bytes"
	"testing"

	"petris.dev/toby/internal/tool"
	"petris.dev/toby/internal/version"

	"github.com/spf13/cobra"
)

func TestSandboxCommandHiddenOnHost(t *testing.T) {
	t.Setenv("TOBY_SANDBOX", "")
	cmd := NewRootCommand(Params{Registry: emptyRegistry(t)})
	sandbox := findCommand(cmd, "sandbox")
	if sandbox == nil {
		t.Fatal("sandbox command missing")
	}
	if !sandbox.Hidden {
		t.Fatal("sandbox command should be hidden on host")
	}
}

func TestSandboxCommandVisibleInsideSandbox(t *testing.T) {
	t.Setenv("TOBY_SANDBOX", "1")
	cmd := NewRootCommand(Params{Registry: emptyRegistry(t)})
	sandbox := findCommand(cmd, "sandbox")
	if sandbox == nil {
		t.Fatal("sandbox command missing")
	}
	if sandbox.Hidden {
		t.Fatal("sandbox command should be visible inside sandbox")
	}
}

func TestExecCommandGeneratedFromRegisteredTool(t *testing.T) {
	registry, err := tool.NewRegistry(tool.RegistryParams{Tools: []tool.Tool{
		configTestTool{Base: tool.Base{Metadata: tool.Metadata{Name: tool.ExecToolName, LaunchHelp: "Run a command"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	cmd := NewRootCommand(Params{Registry: registry})
	if findCommand(cmd, "exec") == nil {
		t.Fatal("exec command missing")
	}
}

func TestVersionFlagPrintsVersion(t *testing.T) {
	old := version.Version
	version.Version = "v1.2.3"
	t.Cleanup(func() { version.Version = old })

	var stdout bytes.Buffer
	cmd := NewRootCommand(Params{Registry: emptyRegistry(t), Args: []string{"--version"}, Stdout: &stdout})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := stdout.String(); got != "v1.2.3\n" {
		t.Fatalf("version output = %q, want %q", got, "v1.2.3\n")
	}
}

func TestVersionFlagDefaultsToDev(t *testing.T) {
	old := version.Version
	version.Version = ""
	t.Cleanup(func() { version.Version = old })

	var stdout bytes.Buffer
	cmd := NewRootCommand(Params{Registry: emptyRegistry(t), Args: []string{"--version"}, Stdout: &stdout})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := stdout.String(); got != "dev\n" {
		t.Fatalf("version output = %q, want %q", got, "dev\n")
	}
}

func emptyRegistry(t *testing.T) *tool.Registry {
	t.Helper()
	registry, err := tool.NewRegistry(tool.RegistryParams{})
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func findCommand(cmd *cobra.Command, name string) *cobra.Command {
	for _, child := range cmd.Commands() {
		if child.Name() == name {
			return child
		}
	}
	return nil
}
