package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"petris.dev/toby/internal/tobyconfig"
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

func TestApplySandboxDefaultsUsesHostDockerDefaults(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	writeTobyConfig(t, dir, []byte(`
sandbox:
  runtime: docker
  docker:
    image: node:host
    home: /home/host
    projects: /workspace/host
`))
	config, err := tobyconfig.Load(dir, home)
	if err != nil {
		t.Fatal(err)
	}

	got := applySandboxDefaults(&tool.CommandOptions{}, config)
	if got.SandboxRuntime != "docker" || got.DockerImage != "node:host" || got.DockerHome != "/home/host" || got.DockerProjects != "/workspace/host" {
		t.Fatalf("defaults = %#v", got)
	}
}

func TestApplySandboxDefaultsPreservesExplicitLaunchValues(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	writeTobyConfig(t, dir, []byte(`
sandbox:
  runtime: docker
  docker:
    image: node:host
    home: /home/host
    projects: /workspace/host
`))
	config, err := tobyconfig.Load(dir, home)
	if err != nil {
		t.Fatal(err)
	}

	got := applySandboxDefaults(&tool.CommandOptions{SandboxRuntime: "docker", DockerImage: "node:launch", DockerHome: "/home/launch", DockerProjects: "/workspace/launch"}, config)
	if got.DockerImage != "node:launch" || got.DockerHome != "/home/launch" || got.DockerProjects != "/workspace/launch" {
		t.Fatalf("defaults = %#v", got)
	}
}

func TestApplySandboxDefaultsDoesNotApplyDormantDockerDefaults(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	writeTobyConfig(t, dir, []byte(`
sandbox:
  docker:
    image: node:host
    home: /home/host
    projects: /workspace/host
`))
	config, err := tobyconfig.Load(dir, home)
	if err != nil {
		t.Fatal(err)
	}

	got := applySandboxDefaults(&tool.CommandOptions{}, config)
	if got.DockerImage != "" || got.DockerHome != "" || got.DockerProjects != "" {
		t.Fatalf("defaults = %#v", got)
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

func writeTobyConfig(t *testing.T, dir string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func findCommand(cmd *cobra.Command, name string) *cobra.Command {
	for _, child := range cmd.Commands() {
		if child.Name() == name {
			return child
		}
	}
	return nil
}
