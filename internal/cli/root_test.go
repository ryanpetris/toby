package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/tobyconfig"
	"petris.dev/toby/internal/tool"
	"petris.dev/toby/internal/version"
	"petris.dev/toby/internal/warning"

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

func TestRootConfigFlagRejectsEmptyValue(t *testing.T) {
	cmd := NewRootCommand(Params{Registry: emptyRegistry(t), Args: []string{"--config", ""}})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--config requires a value") {
		t.Fatalf("err = %v, want config value error", err)
	}
}

func TestLaunchConfigFlagRejectsEmptyValue(t *testing.T) {
	registry, err := tool.NewRegistry(tool.RegistryParams{Tools: []tool.Tool{
		configTestTool{Base: tool.Base{Metadata: tool.Metadata{Name: tool.OpenCodeToolName, LaunchHelp: "Launch OpenCode"}}},
	}})
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
  runtime:
    default: docker
    docker:
      image: node:host
      home: /home/host
      projects: /workspace/host
      build:
        context: docker
        dockerfile: Dockerfile.toby
    bubblewrap:
      root: ./sandboxes
  tools:
    default:
      state: host
      stateRoot: ./state
`))
	config, err := tobyconfig.Load(dir, home)
	if err != nil {
		t.Fatal(err)
	}

	got := applySandboxDefaults(&tool.CommandOptions{}, config)
	if got.SandboxRuntime != "docker" || got.DockerImage != "node:host" || got.DockerHome != "/home/host" || got.DockerProjects != "/workspace/host" {
		t.Fatalf("defaults = %#v", got)
	}
	if got.DockerBuild.Context != filepath.Join(dir, "docker") || got.DockerBuild.Dockerfile != filepath.Join(dir, "docker", "Dockerfile.toby") {
		t.Fatalf("docker build = %#v", got.DockerBuild)
	}
	if got.BubblewrapRoot != filepath.Join(dir, "sandboxes") {
		t.Fatalf("defaults = %#v", got)
	}
	if got.ToolStates.Default.State != tool.ToolStateHost || got.ToolStates.Default.StateRoot != filepath.Join(dir, "state") {
		t.Fatalf("tool states = %#v", got.ToolStates)
	}
}

func TestApplySandboxDefaultsPreservesExplicitLaunchValues(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	writeTobyConfig(t, dir, []byte(`
sandbox:
  runtime:
    default: docker
    docker:
      image: node:host
      home: /home/host
      projects: /workspace/host
      build:
        context: docker
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

func TestApplySandboxDefaultsMergesLaunchToolStateOverrides(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	writeTobyConfig(t, dir, []byte(`
sandbox:
  tools:
    default:
      state: host
      stateRoot: ~/state/default
    claude:
      state: host
      stateRoot: state/claude
`))
	config, err := tobyconfig.Load(dir, home)
	if err != nil {
		t.Fatal(err)
	}

	got := applySandboxDefaults(&tool.CommandOptions{ToolStates: tool.ToolStateSettings{Tools: map[string]tool.ToolStateConfig{tool.ClaudeToolName: {State: tool.ToolStatePrivate}}}}, config)
	if got.ToolStates.StateFor(tool.OpenCodeToolName) != tool.ToolStateHost || got.ToolStates.StateFor(tool.ClaudeToolName) != tool.ToolStatePrivate {
		t.Fatalf("tool states = %#v", got.ToolStates)
	}
	if got.ToolStates.StateRootFor(tool.OpenCodeToolName) != filepath.Join(home, "state", "default") || got.ToolStates.StateRootFor(tool.ClaudeToolName) != filepath.Join(dir, "state", "claude") {
		t.Fatalf("tool roots = %#v", got.ToolStates)
	}
}

func TestApplySandboxDefaultsMergesWarningSuppression(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	writeTobyConfig(t, dir, []byte(`
sandbox:
  suppressWarnings: true
`))
	config, err := tobyconfig.Load(dir, home)
	if err != nil {
		t.Fatal(err)
	}

	got := applySandboxDefaults(&tool.CommandOptions{SuppressWarnings: warning.Suppression{Set: true, IDs: map[warning.ID]bool{warning.ToolHostState: true}}}, config)
	if !got.SuppressWarnings.Suppresses(warning.ToolHostState) || got.SuppressWarnings.Suppresses(warning.OpenCodeModelDiscovery) {
		t.Fatalf("suppress warnings = %#v", got.SuppressWarnings)
	}
}

func TestWarnHostToolStateSkipsDocker(t *testing.T) {
	registry, err := tool.NewRegistry(tool.RegistryParams{Tools: []tool.Tool{
		statefulTestTool{name: tool.OpenCodeToolName},
		statefulTestTool{name: tool.DockerToolName},
	}})
	if err != nil {
		t.Fatal(err)
	}
	toolset, err := registry.Build([]string{tool.OpenCodeToolName, tool.DockerToolName}, "")
	if err != nil {
		t.Fatal(err)
	}
	toolset.SetToolStates(tool.ToolStateSettings{Default: tool.ToolStateConfig{State: tool.ToolStateHost, StateRoot: t.TempDir()}})
	var stderr bytes.Buffer
	warnHostToolState(&stderr, warning.Suppression{}, toolset)
	if got := stderr.String(); got == "" || !bytes.Contains(stderr.Bytes(), []byte("warning[tool.host-state]")) || !bytes.Contains(stderr.Bytes(), []byte(tool.OpenCodeToolName)) || bytes.Contains(stderr.Bytes(), []byte(tool.DockerToolName)) {
		t.Fatalf("warning = %q", got)
	}
	stderr.Reset()
	warnHostToolState(&stderr, warning.Suppression{Set: true, IDs: map[warning.ID]bool{warning.ToolHostState: true}}, toolset)
	if stderr.Len() != 0 {
		t.Fatalf("suppressed warning = %q", stderr.String())
	}

	toolset, err = registry.Build([]string{tool.DockerToolName}, "")
	if err != nil {
		t.Fatal(err)
	}
	toolset.SetToolStates(tool.ToolStateSettings{})
	stderr.Reset()
	warnHostToolState(&stderr, warning.Suppression{}, toolset)
	if stderr.Len() != 0 {
		t.Fatalf("docker warning = %q", stderr.String())
	}
}

func TestApplySandboxDefaultsDoesNotApplyDormantDockerDefaults(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	writeTobyConfig(t, dir, []byte(`
sandbox:
  runtime:
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
	if got.DockerImage != "" || got.DockerHome != "" || got.DockerProjects != "" || got.DockerBuild.IsSet() {
		t.Fatalf("defaults = %#v", got)
	}
}

func TestMaybeAutoloadProjectConfigWarnsWhenDisabled(t *testing.T) {
	home := t.TempDir()
	projectRoot := filepath.Join(home, "Projects")
	project := filepath.Join(projectRoot, "app")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, projectLaunchConfigName), []byte("projects: []\ntools: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := tobyconfig.Load(t.TempDir(), home)
	if err != nil {
		t.Fatal(err)
	}
	parsed := parsedCommand{Options: tool.CommandOptions{Env: "app"}, RequestedTools: []string{tool.OpenCodeToolName}}
	var stderr bytes.Buffer
	_, ok, err := maybeAutoloadProjectConfig(Params{Paths: configPaths(home, projectRoot), TobyConfig: config, Stderr: &stderr}, parsed, tool.OpenCodeToolName)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("autoload should be disabled")
	}
	if got := stderr.String(); !bytes.Contains([]byte(got), []byte("warning[project.autoload-disabled]")) || !bytes.Contains([]byte(got), []byte(projectLaunchConfigName)) {
		t.Fatalf("warning = %q", got)
	}
}

func TestMaybeAutoloadProjectConfigLoadsWhenEnabled(t *testing.T) {
	home := t.TempDir()
	projectRoot := filepath.Join(home, "Projects")
	project := filepath.Join(projectRoot, "app")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, projectLaunchConfigName), []byte(`
sandbox:
  name: review
projects:
  - app
tools:
  - opencode
  - npm
`), 0o600); err != nil {
		t.Fatal(err)
	}
	configDir := t.TempDir()
	writeTobyConfig(t, configDir, []byte(`
sandbox:
  autoloadProjectConfig: true
`))
	config, err := tobyconfig.Load(configDir, home)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := tool.NewRegistry(tool.RegistryParams{Tools: []tool.Tool{
		statefulTestTool{name: tool.OpenCodeToolName},
		statefulTestTool{name: tool.NpmToolName},
	}})
	if err != nil {
		t.Fatal(err)
	}
	parsed := parsedCommand{Options: tool.CommandOptions{Env: "app"}, RequestedTools: []string{tool.OpenCodeToolName}}
	launch, ok, err := maybeAutoloadProjectConfig(Params{Registry: registry, Paths: configPaths(home, projectRoot), TobyConfig: config}, parsed, tool.OpenCodeToolName)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected autoload")
	}
	if launch.Options.Env != "review" || launch.Primary != tool.OpenCodeToolName {
		t.Fatalf("launch = %#v", launch)
	}
	wantTools := []string{tool.OpenCodeToolName, tool.NpmToolName}
	if len(launch.RequestedTools) != len(wantTools) || launch.RequestedTools[0] != wantTools[0] || launch.RequestedTools[1] != wantTools[1] {
		t.Fatalf("requested tools = %#v", launch.RequestedTools)
	}
}

func configPaths(home, projectRoot string) config.Paths {
	return config.Paths{Home: home, XDGConfigHome: filepath.Join(home, ".config"), ProjectRoot: projectRoot, SandboxRoot: filepath.Join(home, ".cache", "toby", "sandboxes")}
}

func emptyRegistry(t *testing.T) *tool.Registry {
	t.Helper()
	registry, err := tool.NewRegistry(tool.RegistryParams{})
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

type statefulTestTool struct{ name string }

func (t statefulTestTool) Name() string { return t.name }

func (t statefulTestTool) CommandName() string { return t.name }

func (t statefulTestTool) LaunchHelp() string { return "Launch " + t.name }

func (t statefulTestTool) ContextGroups() []string { return nil }

func (t statefulTestTool) Binds() []tool.Bind {
	return []tool.Bind{{HostPath: "/host/" + t.name, Target: tool.HomeTarget("." + t.name), State: true}}
}

func (t statefulTestTool) PathEntries() []tool.PathTarget { return nil }

func (t statefulTestTool) ConfigureCommand(*cobra.Command) {}

func (t statefulTestTool) HostInit(context.Context, *tool.CommandOptions) error { return nil }

func (t statefulTestTool) SandboxContextSetup(*tool.RunContext) error { return nil }

func (t statefulTestTool) SandboxInit(context.Context, *tool.RunContext) error { return nil }

func (t statefulTestTool) Install(context.Context, *tool.RunContext) error { return nil }

func (t statefulTestTool) Upgrade(context.Context, *tool.RunContext) error { return nil }

func (t statefulTestTool) Launch(context.Context, *tool.RunContext) error { return nil }

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
