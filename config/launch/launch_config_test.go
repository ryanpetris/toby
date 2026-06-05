package launchconfig

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"petris.dev/toby/config"
	tobyconfig "petris.dev/toby/config/toby"
	"petris.dev/toby/diagnostic/warning"
	"petris.dev/toby/tools"
)

func TestLoadLaunchConfigDefaultsNameAndResolvesProjectPaths(t *testing.T) {
	home := t.TempDir()
	projectRoot := filepath.Join(home, "Projects")
	dir := filepath.Join(home, "configs", "app")
	absolute := filepath.Join(home, "absolute")
	configPath := filepath.Join(dir, "toby.yaml")
	writeTestFile(t, configPath, []byte(`
container:
  image: custom-node
  build:
    context: docker/context
    dockerfile: ../Dockerfile.toby
settings:
  mountProfile: review
  autoUpgrade: true
  debug: true
  yolo: true
  suppressWarnings: ["*"]
workdir: ~/literal-workdir/../raw
project:
  foo:
  named:
  dot:
    path: .
  bar:
    path: ../bar-src
  abs:
    path: `+absolute+`
  tilde:
    path: ~/tilde-source/../raw
tool:
  opencode:
    mountProfile: review
  uv:
  npm:
`))

	cfg, err := loadLaunchConfigWithPaths(configPath, config.Paths{Home: home, ProjectRoot: projectRoot})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "" || !cfg.Settings.AutoUpgrade || cfg.Settings.MountProfile != "review" || cfg.Settings.Debug == nil || !*cfg.Settings.Debug || cfg.Settings.Yolo == nil || !*cfg.Settings.Yolo {
		t.Fatalf("settings/name = %#v %q", cfg.Settings, cfg.Name)
	}
	wantWorkdir := "~/literal-workdir/../raw"
	if cfg.Workdir != wantWorkdir {
		t.Fatalf("workdir = %q", cfg.Workdir)
	}
	if cfg.Container.Image != "custom-node" {
		t.Fatalf("container config = %#v", cfg.Container)
	}
	if cfg.Container.Build.Context != filepath.Join(dir, "docker", "context") || cfg.Container.Build.Dockerfile != filepath.Join(dir, "docker", "Dockerfile.toby") {
		t.Fatalf("container build config = %#v", cfg.Container)
	}
	if !cfg.Settings.SuppressWarnings.Suppresses(warning.MountHostBacking) || !cfg.Settings.SuppressWarnings.Suppresses(warning.OpenCodeModelDiscovery) {
		t.Fatalf("suppress warnings = %#v", cfg.Settings.SuppressWarnings)
	}
	wantProjects := []tools.ProjectMount{
		{Name: "abs", Source: absolute},
		{Name: "bar", Source: dir + string(filepath.Separator) + "../bar-src"},
		{Name: "dot", Source: dir},
		{Name: "foo", Source: filepath.Join(projectRoot, "foo")},
		{Name: "named", Source: filepath.Join(projectRoot, "named")},
		{Name: "tilde", Source: home + "/tilde-source/../raw"},
	}
	if !reflect.DeepEqual(projectMounts(cfg.Projects), wantProjects) {
		t.Fatalf("projects = %#v, want %#v", cfg.Projects, wantProjects)
	}
	wantTools := []launchToolConfig{{Name: "npm", Label: "tool.npm"}, {Name: "opencode", Label: "tool.opencode", MountProfile: "review"}, {Name: "uv", Label: "tool.uv"}}
	if !reflect.DeepEqual(cfg.Tools, wantTools) {
		t.Fatalf("tools = %#v, want %#v", cfg.Tools, wantTools)
	}
}

func TestLoadLaunchConfigParsesJSONWithYAMLParser(t *testing.T) {
	home := t.TempDir()
	projectRoot := filepath.Join(home, "Projects")
	configPath := filepath.Join(home, "toby.json")
	writeTestFile(t, configPath, []byte(`{"name":"json-env","container":{"image":"custom-node"},"project":{"foo":null},"tool":{"opencode":null}}`))

	cfg, err := loadLaunchConfigWithPaths(configPath, config.Paths{Home: home, ProjectRoot: projectRoot})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "json-env" {
		t.Fatalf("name = %q", cfg.Name)
	}
	if cfg.Container.Image != "custom-node" {
		t.Fatalf("container = %#v", cfg.Container)
	}
	if got, want := cfg.Projects[0].Mount.Source, filepath.Join(projectRoot, "foo"); got != want {
		t.Fatalf("project source = %q, want %q", got, want)
	}
}

func TestBuildConfiguredLaunchResolvesCommandNames(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, "toby.yaml")
	writeTestFile(t, configPath, []byte(`
project:
  foo:
workdir: /tmp/work
settings:
  mountProfile: shared
  debug: false
  yolo: true
  suppressWarnings:
    - opencode.model-discovery
tool:
  gh:
    primary: true
  npm:
    mountProfile: shared
`))
	registry, err := tools.NewRegistry([]tools.Tool{
		configTestTool{Base: tools.Base{Metadata: tools.Metadata{Name: tools.GitHubCliToolName, CLIName: "gh", LaunchHelp: "Launch GitHub CLI"}}},
		configTestTool{Base: tools.Base{Metadata: tools.Metadata{Name: tools.NpmToolName, LaunchHelp: "Launch npm"}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	launch, err := BuildConfiguredLaunch(Params{Registry: registry, Paths: config.Paths{Home: home}}, configPath, []string{"--repo", "x"})
	if err != nil {
		t.Fatal(err)
	}
	if launch.Primary != tools.GitHubCliToolName {
		t.Fatalf("primary = %q", launch.Primary)
	}
	wantTools := []string{tools.GitHubCliToolName, tools.NpmToolName}
	if !reflect.DeepEqual(launch.RequestedTools, wantTools) {
		t.Fatalf("requested tools = %#v, want %#v", launch.RequestedTools, wantTools)
	}
	if launch.Options.Env != "" || launch.Options.Workdir != "/tmp/work" || len(launch.Options.Projects) != 1 || launch.Options.Projects[0].Name != "foo" {
		t.Fatalf("options = %#v", launch.Options)
	}
	if launch.Options.Debug == nil || launch.Options.DebugEnabled() {
		t.Fatalf("debug = %#v", launch.Options.Debug)
	}
	if launch.Options.Yolo == nil || !launch.Options.YoloEnabled() {
		t.Fatalf("yolo = %#v", launch.Options.Yolo)
	}
	if launch.Options.MountProfile != "shared" {
		t.Fatalf("mount profile = %q", launch.Options.MountProfile)
	}
	if !launch.Options.SuppressWarnings.Suppresses(warning.OpenCodeModelDiscovery) || launch.Options.SuppressWarnings.Suppresses(warning.MountHostBacking) {
		t.Fatalf("suppress warnings = %#v", launch.Options.SuppressWarnings)
	}
	if launch.Options.ToolMountProfiles[tools.NpmToolName] != "shared" {
		t.Fatalf("tool mount profiles = %#v", launch.Options.ToolMountProfiles)
	}
	if got, want := launch.Extra, []string{"--repo", "x"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("extra = %#v, want %#v", got, want)
	}
}

func TestBuildConfiguredLaunchAppendsCLIArgsAfterPrimaryParams(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, "toby.yaml")
	writeTestFile(t, configPath, []byte(`
project:
  foo:
tool:
  exec:
    primary: true
    params: ["npm", "test"]
  npm:
`))
	registry, err := tools.NewRegistry([]tools.Tool{
		configTestTool{Base: tools.Base{Metadata: tools.Metadata{Name: tools.ExecToolName, LaunchHelp: "Run a command"}}},
		configTestTool{Base: tools.Base{Metadata: tools.Metadata{Name: tools.NpmToolName, LaunchHelp: "Launch npm"}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	launch, err := BuildConfiguredLaunch(Params{Registry: registry, Paths: config.Paths{Home: home}}, configPath, []string{"--", "--watch"})
	if err != nil {
		t.Fatal(err)
	}
	if launch.Primary != tools.ExecToolName {
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
name: custom-name
project:
  duplicate:
    path: Projects/app
  shared:
  extra:
    path: extra
tool:
  opencode:
  npm:
`))
	registry, err := tools.NewRegistry([]tools.Tool{
		configTestTool{Base: tools.Base{Metadata: tools.Metadata{Name: tools.OpenCodeToolName, LaunchHelp: "Launch OpenCode"}}},
		configTestTool{Base: tools.Base{Metadata: tools.Metadata{Name: tools.NpmToolName, LaunchHelp: "Launch npm"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed := DirectLaunch{Options: tools.Options{Env: "app"}, Extra: []string{"--foreground"}, RequestedTools: []string{tools.OpenCodeToolName}}
	paths := config.Paths{Home: home, ProjectRoot: projectRoot}
	primaryProject, err := ResolveDirectLaunchProject(paths, parsed.Options)
	if err != nil {
		t.Fatal(err)
	}
	launch, err := BuildOverlayConfiguredLaunch(Params{Registry: registry, Paths: paths}, configPath, parsed, tools.OpenCodeToolName, primaryProject)
	if err != nil {
		t.Fatal(err)
	}
	if launch.Primary != tools.OpenCodeToolName || !reflect.DeepEqual(launch.RequestedTools, []string{tools.OpenCodeToolName, tools.NpmToolName}) {
		t.Fatalf("tools = primary %q requested %#v", launch.Primary, launch.RequestedTools)
	}
	if launch.Options.Env != "custom-name" || !reflect.DeepEqual(launch.Extra, []string{"--foreground"}) {
		t.Fatalf("launch = %#v extra %#v", launch.Options, launch.Extra)
	}
	wantProjects := []tools.ProjectMount{{Name: "app", Source: project}, {Name: "duplicate", Source: project}, {Name: "extra", Source: extraProject}, {Name: "shared", Source: sharedProject}}
	if !reflect.DeepEqual(launch.Options.Projects, wantProjects) {
		t.Fatalf("projects = %#v, want %#v", launch.Options.Projects, wantProjects)
	}
}

func TestBuildConfiguredLaunchRejectsParamsOnSecondaryTool(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, "toby.yaml")
	writeTestFile(t, configPath, []byte(`
project:
  foo:
tool:
  exec:
    primary: true
  npm:
    params: ["test"]
`))
	registry, err := tools.NewRegistry([]tools.Tool{
		configTestTool{Base: tools.Base{Metadata: tools.Metadata{Name: tools.ExecToolName, LaunchHelp: "Run a command"}}},
		configTestTool{Base: tools.Base{Metadata: tools.Metadata{Name: tools.NpmToolName, LaunchHelp: "Launch npm"}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = BuildConfiguredLaunch(Params{Registry: registry, Paths: config.Paths{Home: home}}, configPath, nil)
	if err == nil || !strings.Contains(err.Error(), "tool.npm.params is only supported on the primary tool") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadLaunchConfigRejectsInvalidSuppressedWarning(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, "toby.yaml")
	writeTestFile(t, configPath, []byte(`
settings:
  suppressWarnings:
    - unknown.warning
project:
  foo:
tool:
  exec:
`))

	_, err := loadLaunchConfig(configPath, home)
	if err == nil || !strings.Contains(err.Error(), "settings.suppressWarnings[0]") {
		t.Fatalf("error = %v", err)
	}
}

func TestBuildConfiguredLaunchRejectsUnknownTools(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, "toby.yaml")
	writeTestFile(t, configPath, []byte(`
project:
  foo:
tool:
  unknown-command:
`))
	registry, err := tools.NewRegistry(nil)
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
	writeTestFile(t, filepath.Join(project, projectLaunchConfigName), []byte("project: {}\ntool: {}\n"))
	cfgSvc, err := tobyconfig.Load(t.TempDir(), home)
	if err != nil {
		t.Fatal(err)
	}
	parsed := DirectLaunch{Options: tools.Options{Env: "app"}, RequestedTools: []string{tools.OpenCodeToolName}}
	var stderr bytes.Buffer
	_, ok, err := MaybeAutoloadProjectConfig(Params{Paths: configPaths(home, projectRoot), Config: cfgSvc, Stderr: &stderr}, parsed, tools.OpenCodeToolName)
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
name: review
project:
  sibling:
tool:
  opencode:
  npm:
`))
	configDir := t.TempDir()
	writeTestFile(t, filepath.Join(configDir, "config.yaml"), []byte(`
settings:
  autoloadProjectConfig: true
`))
	cfgSvc, err := tobyconfig.Load(configDir, home)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := tools.NewRegistry([]tools.Tool{
		configTestTool{Base: tools.Base{Metadata: tools.Metadata{Name: tools.OpenCodeToolName, LaunchHelp: "Launch OpenCode"}}},
		configTestTool{Base: tools.Base{Metadata: tools.Metadata{Name: tools.NpmToolName, LaunchHelp: "Launch npm"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed := DirectLaunch{Options: tools.Options{Env: "app"}, RequestedTools: []string{tools.OpenCodeToolName}}
	launch, ok, err := MaybeAutoloadProjectConfig(Params{Registry: registry, Paths: configPaths(home, projectRoot), Config: cfgSvc}, parsed, tools.OpenCodeToolName)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected autoload")
	}
	if launch.Options.Env != "review" || launch.Primary != tools.OpenCodeToolName {
		t.Fatalf("launch = %#v", launch)
	}
	wantTools := []string{tools.OpenCodeToolName, tools.NpmToolName}
	if len(launch.RequestedTools) != len(wantTools) || launch.RequestedTools[0] != wantTools[0] || launch.RequestedTools[1] != wantTools[1] {
		t.Fatalf("requested tools = %#v", launch.RequestedTools)
	}
	wantProjects := []tools.ProjectMount{{Name: "app", Source: project}, {Name: "sibling", Source: sibling}}
	if !reflect.DeepEqual(launch.Options.Projects, wantProjects) {
		t.Fatalf("projects = %#v, want %#v", launch.Options.Projects, wantProjects)
	}
}

func configPaths(home, projectRoot string) config.Paths {
	return config.Paths{Home: home, XDGConfigHome: filepath.Join(home, ".config"), ProjectRoot: projectRoot, SandboxRoot: filepath.Join(home, ".cache", "toby", "sandboxes")}
}

type configTestTool struct{ tools.Base }

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
