package cli

import (
	"bytes"
	"strings"
	"testing"

	"petris.dev/toby/internal/version"
	"petris.dev/toby/tools"

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
	registry, err := tools.NewRegistry([]tools.Tool{
		configTestTool{Base: tools.Base{Metadata: tools.Metadata{Name: "exec", Group: tools.GroupCommand, LaunchHelp: "Run a command"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd := NewRootCommand(Params{Registry: registry})
	if findCommand(cmd, "exec") == nil {
		t.Fatal("exec command missing")
	}
}

func TestRootConfigFlagRejectsEmptyValue(t *testing.T) {
	cmd := NewRootCommand(Params{Registry: emptyRegistry(t), Args: []string{"--config", ""}})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--config requires a value") {
		t.Fatalf("err = %v, want config value error", err)
	}
}

func TestLaunchConfigFlagRejectsEmptyValue(t *testing.T) {
	registry, err := tools.NewRegistry([]tools.Tool{
		configTestTool{Base: tools.Base{Metadata: tools.Metadata{Name: "opencode", LaunchHelp: "Launch OpenCode"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd := NewRootCommand(Params{Registry: registry, Args: []string{"opencode", "env", "--config", ""}})
	err = cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--config requires a value") {
		t.Fatalf("err = %v, want config value error", err)
	}
}

func TestVersionFlagPrintsVersion(t *testing.T) {
	old := version.Current
	version.Current = "v1.2.3"
	t.Cleanup(func() { version.Current = old })

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
	old := version.Current
	version.Current = ""
	t.Cleanup(func() { version.Current = old })

	var stdout bytes.Buffer
	cmd := NewRootCommand(Params{Registry: emptyRegistry(t), Args: []string{"--version"}, Stdout: &stdout})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := stdout.String(); got != "dev\n" {
		t.Fatalf("version output = %q, want %q", got, "dev\n")
	}
}

func emptyRegistry(t *testing.T) *tools.Registry {
	t.Helper()
	registry, err := tools.NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

type configTestTool struct{ tools.Base }

func findCommand(cmd *cobra.Command, name string) *cobra.Command {
	for _, child := range cmd.Commands() {
		if child.Name() == name {
			return child
		}
	}
	return nil
}
