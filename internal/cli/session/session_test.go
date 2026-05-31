package session

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"petris.dev/toby/internal/config/toby"
	"petris.dev/toby/internal/diagnostic/warning"
	"petris.dev/toby/internal/tools/tool"

	"github.com/spf13/cobra"
)

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
	cfg, err := tobyconfig.Load(dir, home)
	if err != nil {
		t.Fatal(err)
	}

	got := applySandboxDefaults(&tool.CommandOptions{}, cfg)
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
	cfg, err := tobyconfig.Load(dir, home)
	if err != nil {
		t.Fatal(err)
	}

	got := applySandboxDefaults(&tool.CommandOptions{SandboxRuntime: "docker", DockerImage: "node:launch", DockerHome: "/home/launch", DockerProjects: "/workspace/launch"}, cfg)
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
	cfg, err := tobyconfig.Load(dir, home)
	if err != nil {
		t.Fatal(err)
	}

	got := applySandboxDefaults(&tool.CommandOptions{ToolStates: tool.ToolStateSettings{Tools: map[string]tool.ToolStateConfig{tool.ClaudeToolName: {State: tool.ToolStatePrivate}}}}, cfg)
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
	cfg, err := tobyconfig.Load(dir, home)
	if err != nil {
		t.Fatal(err)
	}

	got := applySandboxDefaults(&tool.CommandOptions{SuppressWarnings: warning.Suppression{Set: true, IDs: map[warning.ID]bool{warning.ToolHostState: true}}}, cfg)
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
	cfg, err := tobyconfig.Load(dir, home)
	if err != nil {
		t.Fatal(err)
	}

	got := applySandboxDefaults(&tool.CommandOptions{}, cfg)
	if got.DockerImage != "" || got.DockerHome != "" || got.DockerProjects != "" || got.DockerBuild.IsSet() {
		t.Fatalf("defaults = %#v", got)
	}
}

func TestPrepareConfiguredProjectsWarnsAndSkipsMissingProjects(t *testing.T) {
	home := t.TempDir()
	existing := filepath.Join(home, "existing")
	missing := filepath.Join(home, "missing")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	opts := &tool.CommandOptions{Projects: []tool.ProjectMount{{Name: "missing", Source: missing}, {Name: "existing", Source: existing}}}
	var stderr bytes.Buffer
	if err := prepareConfiguredProjects(&stderr, home, opts); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "warning[project.missing]") || !strings.Contains(stderr.String(), missing) {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if opts.Env != "existing" || !reflect.DeepEqual(opts.Projects, []tool.ProjectMount{{Name: "existing", Source: existing}}) {
		t.Fatalf("options = %#v", opts)
	}

	stderr.Reset()
	opts = &tool.CommandOptions{SuppressWarnings: warning.Suppression{Set: true, IDs: map[warning.ID]bool{warning.ProjectMissing: true}}, Projects: []tool.ProjectMount{{Name: "missing", Source: missing}}}
	if err := prepareConfiguredProjects(&stderr, home, opts); err == nil || !strings.Contains(err.Error(), "at least one existing project") {
		t.Fatalf("error = %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("suppressed stderr = %q", stderr.String())
	}
}

func TestPrepareConfiguredProjectsWarnsAndSkipsDuplicateNames(t *testing.T) {
	home := t.TempDir()
	first := filepath.Join(home, "first")
	second := filepath.Join(home, "second")
	if err := os.MkdirAll(first, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(second, 0o755); err != nil {
		t.Fatal(err)
	}
	opts := &tool.CommandOptions{Projects: []tool.ProjectMount{{Name: "app", Source: first}, {Name: "app", Source: second}}}
	var stderr bytes.Buffer
	if err := prepareConfiguredProjects(&stderr, home, opts); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "warning[project.duplicate]") || !strings.Contains(stderr.String(), second) {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if opts.Env != "app" || !reflect.DeepEqual(opts.Projects, []tool.ProjectMount{{Name: "app", Source: first}}) {
		t.Fatalf("options = %#v", opts)
	}

	stderr.Reset()
	opts = &tool.CommandOptions{SuppressWarnings: warning.Suppression{Set: true, IDs: map[warning.ID]bool{warning.ProjectDuplicate: true}}, Projects: []tool.ProjectMount{{Name: "app", Source: first}, {Name: "app", Source: second}}}
	if err := prepareConfiguredProjects(&stderr, home, opts); err != nil {
		t.Fatal(err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("suppressed stderr = %q", stderr.String())
	}
}

func TestPrepareConfiguredProjectsAllowsSameSourceWithDifferentNames(t *testing.T) {
	home := t.TempDir()
	source := filepath.Join(home, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	opts := &tool.CommandOptions{Projects: []tool.ProjectMount{{Name: "foo", Source: source}, {Name: "bar", Source: source}}}
	var stderr bytes.Buffer
	if err := prepareConfiguredProjects(&stderr, home, opts); err != nil {
		t.Fatal(err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
	want := []tool.ProjectMount{{Name: "foo", Source: source}, {Name: "bar", Source: source}}
	if opts.Env != "foo" || !reflect.DeepEqual(opts.Projects, want) {
		t.Fatalf("options = %#v, want projects %#v", opts, want)
	}
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

func (t statefulTestTool) SandboxContextSetup(context.Context) error { return nil }

func (t statefulTestTool) SandboxInit(context.Context) error { return nil }

func (t statefulTestTool) Install(context.Context) error { return nil }

func (t statefulTestTool) Upgrade(context.Context) error { return nil }

func (t statefulTestTool) Launch(context.Context, []string) error { return nil }

func writeTobyConfig(t *testing.T, dir string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}
