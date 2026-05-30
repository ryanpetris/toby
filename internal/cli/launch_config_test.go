package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/tool"
	"petris.dev/toby/internal/warning"
)

func TestLoadLaunchConfigDefaultsSandboxNameAndResolvesProjectPaths(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "configs", "app")
	absolute := filepath.Join(home, "absolute")
	configPath := filepath.Join(dir, "toby.yaml")
	writeTestFile(t, configPath, []byte(`
sandbox:
  autoUpgrade: true
  runtime:
    default: docker
    docker:
      image: custom-node
      home: /home/custom
      projects: /workspace/custom
      build:
        context: docker/context
        dockerfile: ../Dockerfile.toby
    bubblewrap:
      root: sandboxes/review
  tools:
    default:
      state: private
      stateRoot: ~/unused-default
    opencode:
      state: host
      stateRoot: state/opencode
  suppressWarnings: true
workdir: ~/literal-workdir/../raw
projects:
  - foo
  - name: bar
    path: ../bar-src
  - name: abs
    path: `+absolute+`
  - name: tilde
    path: ~/tilde-source/../raw
tools:
  - opencode
  - uv
  - npm
`))

	cfg, err := loadLaunchConfig(configPath, home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sandbox.Name != "" || !cfg.Sandbox.AutoUpgrade {
		t.Fatalf("sandbox = %#v", cfg.Sandbox)
	}
	wantWorkdir := "~/literal-workdir/../raw"
	if cfg.Workdir != wantWorkdir {
		t.Fatalf("workdir = %q", cfg.Workdir)
	}
	if cfg.Sandbox.Runtime.Default != "docker" || cfg.Sandbox.Runtime.Docker.Image != "custom-node" || cfg.Sandbox.Runtime.Docker.Home != "/home/custom" || cfg.Sandbox.Runtime.Docker.Projects != "/workspace/custom" {
		t.Fatalf("sandbox docker config = %#v", cfg.Sandbox)
	}
	if cfg.Sandbox.Runtime.Docker.Build.Context != filepath.Join(dir, "docker", "context") || cfg.Sandbox.Runtime.Docker.Build.Dockerfile != filepath.Join(dir, "docker", "Dockerfile.toby") {
		t.Fatalf("sandbox docker build config = %#v", cfg.Sandbox)
	}
	if cfg.Sandbox.Runtime.Bubblewrap.Root != filepath.Join(dir, "sandboxes", "review") {
		t.Fatalf("sandbox bubblewrap config = %#v", cfg.Sandbox)
	}
	if cfg.Sandbox.Tools.Default.State != tool.ToolStatePrivate || cfg.Sandbox.Tools.StateFor("opencode") != tool.ToolStateHost {
		t.Fatalf("sandbox tools = %#v", cfg.Sandbox.Tools)
	}
	if cfg.Sandbox.Tools.Default.StateRoot != filepath.Join(home, "unused-default") || cfg.Sandbox.Tools.StateRootFor("opencode") != filepath.Join(dir, "state", "opencode") {
		t.Fatalf("sandbox tool roots = %#v", cfg.Sandbox.Tools)
	}
	if !cfg.Sandbox.SuppressWarnings.Suppresses(warning.ToolHostState) || !cfg.Sandbox.SuppressWarnings.Suppresses(warning.OpenCodeModelDiscovery) {
		t.Fatalf("suppress warnings = %#v", cfg.Sandbox.SuppressWarnings)
	}
	wantProjects := []tool.ProjectMount{
		{Name: "foo", Source: dir},
		{Name: "bar", Source: dir + string(filepath.Separator) + "../bar-src"},
		{Name: "abs", Source: absolute},
		{Name: "tilde", Source: home + "/tilde-source/../raw"},
	}
	if !reflect.DeepEqual(cfg.Projects, wantProjects) {
		t.Fatalf("projects = %#v, want %#v", cfg.Projects, wantProjects)
	}
	wantTools := []launchToolConfig{{Name: "opencode"}, {Name: "uv"}, {Name: "npm"}}
	if !reflect.DeepEqual(cfg.Tools, wantTools) {
		t.Fatalf("tools = %#v, want %#v", cfg.Tools, wantTools)
	}
}

func TestLoadLaunchConfigParsesJSONWithYAMLParser(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, "toby.json")
	writeTestFile(t, configPath, []byte(`{"sandbox":{"name":"json-env","runtime":"bubblewrap"},"projects":["foo"],"tools":["opencode"]}`))

	cfg, err := loadLaunchConfig(configPath, home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sandbox.Name != "json-env" {
		t.Fatalf("sandbox name = %q", cfg.Sandbox.Name)
	}
	if cfg.Sandbox.Runtime.Default != "bubblewrap" {
		t.Fatalf("runtime = %#v", cfg.Sandbox.Runtime)
	}
	if got, want := cfg.Projects[0].Source, home; got != want {
		t.Fatalf("project source = %q, want %q", got, want)
	}
}

func TestBuildConfiguredLaunchResolvesCommandNames(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, "toby.yaml")
	writeTestFile(t, configPath, []byte(`
projects:
  - foo
workdir: /tmp/work
sandbox:
  tools:
    claude:
      state: host
      stateRoot: ./claude-state
  suppressWarnings:
    - opencode.model-discovery
tools:
  - gh
  - npm
`))
	registry, err := tool.NewRegistry(tool.RegistryParams{Tools: []tool.Tool{
		configTestTool{Base: tool.Base{Metadata: tool.Metadata{Name: tool.GitHubCliToolName, CLIName: "gh", LaunchHelp: "Launch GitHub CLI"}}},
		configTestTool{Base: tool.Base{Metadata: tool.Metadata{Name: tool.NpmToolName, LaunchHelp: "Launch npm"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}

	launch, err := buildConfiguredLaunch(Params{Registry: registry, Paths: config.Paths{Home: home}}, configPath, []string{"--repo", "x"})
	if err != nil {
		t.Fatal(err)
	}
	if launch.Primary != tool.GitHubCliToolName {
		t.Fatalf("primary = %q", launch.Primary)
	}
	wantTools := []string{tool.GitHubCliToolName, tool.NpmToolName}
	if !reflect.DeepEqual(launch.RequestedTools, wantTools) {
		t.Fatalf("requested tools = %#v, want %#v", launch.RequestedTools, wantTools)
	}
	if launch.Options.Env != "" || launch.Options.Workdir != "/tmp/work" || len(launch.Options.Projects) != 1 || launch.Options.Projects[0].Name != "foo" {
		t.Fatalf("options = %#v", launch.Options)
	}
	if launch.Options.ToolStates.StateFor("claude") != tool.ToolStateHost {
		t.Fatalf("tool states = %#v", launch.Options.ToolStates)
	}
	if !launch.Options.SuppressWarnings.Suppresses(warning.OpenCodeModelDiscovery) || launch.Options.SuppressWarnings.Suppresses(warning.ToolHostState) {
		t.Fatalf("suppress warnings = %#v", launch.Options.SuppressWarnings)
	}
	if launch.Options.ToolStates.StateRootFor("claude") != filepath.Join(home, "claude-state") {
		t.Fatalf("tool roots = %#v", launch.Options.ToolStates)
	}
	if got, want := launch.Extra, []string{"--repo", "x"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("extra = %#v, want %#v", got, want)
	}
}

func TestBuildConfiguredLaunchAppendsCLIArgsAfterPrimaryParams(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, "toby.yaml")
	writeTestFile(t, configPath, []byte(`
projects:
  - foo
tools:
  - name: exec
    params: ["npm", "test"]
  - npm
`))
	registry, err := tool.NewRegistry(tool.RegistryParams{Tools: []tool.Tool{
		configTestTool{Base: tool.Base{Metadata: tool.Metadata{Name: tool.ExecToolName, LaunchHelp: "Run a command"}}},
		configTestTool{Base: tool.Base{Metadata: tool.Metadata{Name: tool.NpmToolName, LaunchHelp: "Launch npm"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}

	launch, err := buildConfiguredLaunch(Params{Registry: registry, Paths: config.Paths{Home: home}}, configPath, []string{"--", "--watch"})
	if err != nil {
		t.Fatal(err)
	}
	if launch.Primary != tool.ExecToolName {
		t.Fatalf("primary = %q", launch.Primary)
	}
	wantExtra := []string{"npm", "test", "--", "--watch"}
	if !reflect.DeepEqual(launch.Extra, wantExtra) {
		t.Fatalf("extra = %#v, want %#v", launch.Extra, wantExtra)
	}
}

func TestBuildOverlayConfiguredLaunchKeepsCLIPrimaryAndAddsConfigToolsProjects(t *testing.T) {
	home := t.TempDir()
	projectRoot := filepath.Join(home, "Projects")
	project := filepath.Join(projectRoot, "app")
	extraProject := filepath.Join(home, "extra")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(extraProject, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(home, "config.yaml")
	writeTestFile(t, configPath, []byte(`
sandbox:
  name: custom-name
projects:
  - name: duplicate
    path: Projects/app
  - name: extra
    path: extra
tools:
  - opencode
  - npm
`))
	registry, err := tool.NewRegistry(tool.RegistryParams{Tools: []tool.Tool{
		configTestTool{Base: tool.Base{Metadata: tool.Metadata{Name: tool.OpenCodeToolName, LaunchHelp: "Launch OpenCode"}}},
		configTestTool{Base: tool.Base{Metadata: tool.Metadata{Name: tool.NpmToolName, LaunchHelp: "Launch npm"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseSandboxArgs([]string{"app", "--", "--foreground"}, true, tool.OpenCodeToolName, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	paths := config.Paths{Home: home, ProjectRoot: projectRoot}
	primaryProject, err := resolveDirectLaunchProject(paths, parsed.Options)
	if err != nil {
		t.Fatal(err)
	}
	launch, err := buildOverlayConfiguredLaunch(Params{Registry: registry, Paths: paths}, configPath, parsed, tool.OpenCodeToolName, primaryProject)
	if err != nil {
		t.Fatal(err)
	}
	if launch.Primary != tool.OpenCodeToolName || !reflect.DeepEqual(launch.RequestedTools, []string{tool.OpenCodeToolName, tool.NpmToolName}) {
		t.Fatalf("tools = primary %q requested %#v", launch.Primary, launch.RequestedTools)
	}
	if launch.Options.Env != "custom-name" || !reflect.DeepEqual(launch.Extra, []string{"--foreground"}) {
		t.Fatalf("launch = %#v extra %#v", launch.Options, launch.Extra)
	}
	var stderr bytes.Buffer
	if err := prepareConfiguredProjects(&stderr, home, &launch.Options); err != nil {
		t.Fatal(err)
	}
	wantProjects := []tool.ProjectMount{{Name: "app", Source: project}, {Name: "extra", Source: extraProject}}
	if !reflect.DeepEqual(launch.Options.Projects, wantProjects) {
		t.Fatalf("projects = %#v, want %#v", launch.Options.Projects, wantProjects)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
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

func TestLoadLaunchConfigRejectsParamsOnSecondaryTool(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, "toby.yaml")
	writeTestFile(t, configPath, []byte(`
projects:
  - foo
tools:
  - exec
  - name: npm
    params: ["test"]
`))

	_, err := loadLaunchConfig(configPath, home)
	if err == nil || !strings.Contains(err.Error(), "tools[1].params is only supported on the primary tool") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadLaunchConfigRejectsInvalidSuppressedWarning(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, "toby.yaml")
	writeTestFile(t, configPath, []byte(`
sandbox:
  suppressWarnings:
    - unknown.warning
projects:
  - foo
tools:
  - exec
`))

	_, err := loadLaunchConfig(configPath, home)
	if err == nil || !strings.Contains(err.Error(), "sandbox.suppressWarnings[0]") {
		t.Fatalf("error = %v", err)
	}
}

func TestBuildConfiguredLaunchRejectsUnknownTools(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, "toby.yaml")
	writeTestFile(t, configPath, []byte(`
projects:
  - foo
tools:
  - unknown-command
`))
	registry, err := tool.NewRegistry(tool.RegistryParams{})
	if err != nil {
		t.Fatal(err)
	}

	_, err = buildConfiguredLaunch(Params{Registry: registry, Paths: config.Paths{Home: home}}, configPath, nil)
	if err == nil || !strings.Contains(err.Error(), "unknown tool: unknown-command") {
		t.Fatalf("error = %v", err)
	}
}

type configTestTool struct{ tool.Base }

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
