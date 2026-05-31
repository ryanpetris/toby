package launchconfig

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/config/toby"
	"petris.dev/toby/internal/diagnostic/warning"
	"petris.dev/toby/internal/tools/tool"
)

func TestLoadLaunchConfigDefaultsSandboxNameAndResolvesProjectPaths(t *testing.T) {
	home := t.TempDir()
	projectRoot := filepath.Join(home, "Projects")
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
  - name: named
  - name: dot
    path: .
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

	cfg, err := loadLaunchConfigWithPaths(configPath, config.Paths{Home: home, ProjectRoot: projectRoot})
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
		{Name: "foo", Source: filepath.Join(projectRoot, "foo")},
		{Name: "named", Source: filepath.Join(projectRoot, "named")},
		{Name: "dot", Source: dir},
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
	projectRoot := filepath.Join(home, "Projects")
	configPath := filepath.Join(home, "toby.json")
	writeTestFile(t, configPath, []byte(`{"sandbox":{"name":"json-env","runtime":"bubblewrap"},"projects":["foo"],"tools":["opencode"]}`))

	cfg, err := loadLaunchConfigWithPaths(configPath, config.Paths{Home: home, ProjectRoot: projectRoot})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sandbox.Name != "json-env" {
		t.Fatalf("sandbox name = %q", cfg.Sandbox.Name)
	}
	if cfg.Sandbox.Runtime.Default != "bubblewrap" {
		t.Fatalf("runtime = %#v", cfg.Sandbox.Runtime)
	}
	if got, want := cfg.Projects[0].Source, filepath.Join(projectRoot, "foo"); got != want {
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

	launch, err := BuildConfiguredLaunch(Params{Registry: registry, Paths: config.Paths{Home: home}}, configPath, []string{"--repo", "x"})
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

	launch, err := BuildConfiguredLaunch(Params{Registry: registry, Paths: config.Paths{Home: home}}, configPath, []string{"--", "--watch"})
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
	sharedProject := filepath.Join(projectRoot, "shared")
	extraProject := filepath.Join(home, "extra")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sharedProject, 0o755); err != nil {
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
  - shared
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
	parsed := DirectLaunch{Options: tool.CommandOptions{Env: "app"}, Extra: []string{"--foreground"}, RequestedTools: []string{tool.OpenCodeToolName}}
	paths := config.Paths{Home: home, ProjectRoot: projectRoot}
	primaryProject, err := ResolveDirectLaunchProject(paths, parsed.Options)
	if err != nil {
		t.Fatal(err)
	}
	launch, err := BuildOverlayConfiguredLaunch(Params{Registry: registry, Paths: paths}, configPath, parsed, tool.OpenCodeToolName, primaryProject)
	if err != nil {
		t.Fatal(err)
	}
	if launch.Primary != tool.OpenCodeToolName || !reflect.DeepEqual(launch.RequestedTools, []string{tool.OpenCodeToolName, tool.NpmToolName}) {
		t.Fatalf("tools = primary %q requested %#v", launch.Primary, launch.RequestedTools)
	}
	if launch.Options.Env != "custom-name" || !reflect.DeepEqual(launch.Extra, []string{"--foreground"}) {
		t.Fatalf("launch = %#v extra %#v", launch.Options, launch.Extra)
	}
	wantProjects := []tool.ProjectMount{{Name: "app", Source: project}, {Name: "duplicate", Source: project}, {Name: "shared", Source: sharedProject}, {Name: "extra", Source: extraProject}}
	if !reflect.DeepEqual(launch.Options.Projects, wantProjects) {
		t.Fatalf("projects = %#v, want %#v", launch.Options.Projects, wantProjects)
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

	_, err = BuildConfiguredLaunch(Params{Registry: registry, Paths: config.Paths{Home: home}}, configPath, nil)
	if err == nil || !strings.Contains(err.Error(), "unknown tool: unknown-command") {
		t.Fatalf("error = %v", err)
	}
}

func TestMaybeAutoloadProjectConfigWarnsWhenDisabled(t *testing.T) {
	home := t.TempDir()
	projectRoot := filepath.Join(home, "Projects")
	project := filepath.Join(projectRoot, "app")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(project, projectLaunchConfigName), []byte("projects: []\ntools: []\n"))
	cfgSvc, err := tobyconfig.Load(t.TempDir(), home)
	if err != nil {
		t.Fatal(err)
	}
	parsed := DirectLaunch{Options: tool.CommandOptions{Env: "app"}, RequestedTools: []string{tool.OpenCodeToolName}}
	var stderr bytes.Buffer
	_, ok, err := MaybeAutoloadProjectConfig(Params{Paths: configPaths(home, projectRoot), Config: cfgSvc, Stderr: &stderr}, parsed, tool.OpenCodeToolName)
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
	sibling := filepath.Join(projectRoot, "sibling")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(project, projectLaunchConfigName), []byte(`
sandbox:
  name: review
projects:
  - sibling
tools:
  - opencode
  - npm
`))
	configDir := t.TempDir()
	writeTestFile(t, filepath.Join(configDir, "config.yaml"), []byte(`
sandbox:
  autoloadProjectConfig: true
`))
	cfgSvc, err := tobyconfig.Load(configDir, home)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := tool.NewRegistry(tool.RegistryParams{Tools: []tool.Tool{
		configTestTool{Base: tool.Base{Metadata: tool.Metadata{Name: tool.OpenCodeToolName, LaunchHelp: "Launch OpenCode"}}},
		configTestTool{Base: tool.Base{Metadata: tool.Metadata{Name: tool.NpmToolName, LaunchHelp: "Launch npm"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	parsed := DirectLaunch{Options: tool.CommandOptions{Env: "app"}, RequestedTools: []string{tool.OpenCodeToolName}}
	launch, ok, err := MaybeAutoloadProjectConfig(Params{Registry: registry, Paths: configPaths(home, projectRoot), Config: cfgSvc}, parsed, tool.OpenCodeToolName)
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
	wantProjects := []tool.ProjectMount{{Name: "app", Source: project}, {Name: "sibling", Source: sibling}}
	if !reflect.DeepEqual(launch.Options.Projects, wantProjects) {
		t.Fatalf("projects = %#v, want %#v", launch.Options.Projects, wantProjects)
	}
}

func configPaths(home, projectRoot string) config.Paths {
	return config.Paths{Home: home, XDGConfigHome: filepath.Join(home, ".config"), ProjectRoot: projectRoot, SandboxRoot: filepath.Join(home, ".cache", "toby", "sandboxes")}
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
