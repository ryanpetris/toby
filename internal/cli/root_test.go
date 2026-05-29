package cli

import (
	"testing"

	"petris.dev/toby/internal/tool"

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
