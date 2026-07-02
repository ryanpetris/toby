package launchconfig

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"petris.dev/toby/config"
	"petris.dev/toby/diagnostic/warning"
	appconfig "petris.dev/toby/internal/config/app"
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
  homeProfile: review
  autoUpgrade: true
  debug: true
  yolo: true
  suppressWarnings: ["*"]
workdir: ~/literal-workdir/../raw
projects:
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
tools:
  opencode:
  uv:
  npm:
`))

	cfg, err := loadLaunchConfigWithPaths(configPath, config.Paths{Home: home, ProjectRoot: projectRoot})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "" || !cfg.Settings.AutoUpgrade || cfg.Settings.HomeProfile != "review" || cfg.Settings.Debug == nil || !*cfg.Settings.Debug || cfg.Settings.Yolo == nil || !*cfg.Settings.Yolo {
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
	if !cfg.Settings.SuppressWarnings.Suppresses(warning.MountHostBacking) || !cfg.Settings.SuppressWarnings.Suppresses(warning.ModelDiscovery) {
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
	wantTools := []launchToolConfig{{Name: "npm", Label: "tools.npm"}, {Name: "opencode", Label: "tools.opencode"}, {Name: "uv", Label: "tools.uv"}}
	if !reflect.DeepEqual(cfg.Tools, wantTools) {
		t.Fatalf("tools = %#v, want %#v", cfg.Tools, wantTools)
	}
}

func TestLoadLaunchConfigParsesJSONWithYAMLParser(t *testing.T) {
	home := t.TempDir()
	projectRoot := filepath.Join(home, "Projects")
	configPath := filepath.Join(home, "toby.json")
	writeTestFile(t, configPath, []byte(`{"name":"json-env","container":{"image":"custom-node"},"projects":{"foo":null},"tools":{"opencode":null}}`))

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
projects:
  foo:
workdir: /tmp/work
settings:
  homeProfile: shared
  debug: false
  yolo: true
  suppressWarnings:
    - provider.model-discovery
tools:
  gh:
    primary: true
  npm:
`))
	registry, err := tools.NewRegistry([]tools.Tool{
		configTestTool{Base: tools.Base{Metadata: tools.Metadata{Name: "github_cli", CLIName: "gh", LaunchHelp: "Launch GitHub CLI"}}},
		configTestTool{Base: tools.Base{Metadata: tools.Metadata{Name: "npm", LaunchHelp: "Launch npm"}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	launch, err := BuildConfiguredLaunch(Params{Registry: registry, Paths: config.Paths{Home: home}}, configPath, []string{"--repo", "x"})
	if err != nil {
		t.Fatal(err)
	}
	if launch.Primary != "github_cli" {
		t.Fatalf("primary = %q", launch.Primary)
	}
	wantTools := []string{"github_cli", "npm"}
	if !reflect.DeepEqual(launch.RequestedTools, wantTools) {
		t.Fatalf("requested tools = %#v, want %#v", launch.RequestedTools, wantTools)
	}
	if launch.Options.Env != "" || launch.Options.Workdir != "/tmp/work" || len(launch.Options.Projects) != 1 || launch.Options.Projects[0].Name != "foo" {
		t.Fatalf("options = %#v", launch.Options)
	}
	if launch.Overrides.Debug == nil || *launch.Overrides.Debug {
		t.Fatalf("debug = %#v", launch.Overrides.Debug)
	}
	if launch.Overrides.Yolo == nil || !*launch.Overrides.Yolo {
		t.Fatalf("yolo = %#v", launch.Overrides.Yolo)
	}
	if launch.Overrides.HomeProfile != "shared" {
		t.Fatalf("home profile = %q", launch.Overrides.HomeProfile)
	}
	if !launch.Overrides.SuppressWarnings.Suppresses(warning.ModelDiscovery) || launch.Overrides.SuppressWarnings.Suppresses(warning.MountHostBacking) {
		t.Fatalf("suppress warnings = %#v", launch.Overrides.SuppressWarnings)
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
  foo:
tools:
  exec:
    primary: true
    params: ["npm", "test"]
  npm:
`))
	registry, err := tools.NewRegistry([]tools.Tool{
		configTestTool{Base: tools.Base{Metadata: tools.Metadata{Name: "exec", LaunchHelp: "Run a command"}}},
		configTestTool{Base: tools.Base{Metadata: tools.Metadata{Name: "npm", LaunchHelp: "Launch npm"}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	launch, err := BuildConfiguredLaunch(Params{Registry: registry, Paths: config.Paths{Home: home}}, configPath, []string{"--", "--watch"})
	if err != nil {
		t.Fatal(err)
	}
	if launch.Primary != "exec" {
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
projects:
  duplicate:
    path: Projects/app
  shared:
  extra:
    path: extra
tools:
  opencode:
  npm:
`))
	registry, err := tools.NewRegistry([]tools.Tool{
		configTestTool{Base: tools.Base{Metadata: tools.Metadata{Name: "opencode", LaunchHelp: "Launch OpenCode"}}},
		configTestTool{Base: tools.Base{Metadata: tools.Metadata{Name: "npm", LaunchHelp: "Launch npm"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed := DirectLaunch{Options: tools.Options{Env: "app"}, Extra: []string{"--foreground"}, RequestedTools: []string{"opencode"}}
	paths := config.Paths{Home: home, ProjectRoot: projectRoot}
	primaryProject, err := ResolveDirectLaunchProject(paths, parsed.Options)
	if err != nil {
		t.Fatal(err)
	}
	launch, err := BuildOverlayConfiguredLaunch(Params{Registry: registry, Paths: paths}, configPath, parsed, "opencode", primaryProject)
	if err != nil {
		t.Fatal(err)
	}
	if launch.Primary != "opencode" || !reflect.DeepEqual(launch.RequestedTools, []string{"opencode", "npm"}) {
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
projects:
  foo:
tools:
  exec:
    primary: true
  npm:
    params: ["test"]
`))
	registry, err := tools.NewRegistry([]tools.Tool{
		configTestTool{Base: tools.Base{Metadata: tools.Metadata{Name: "exec", LaunchHelp: "Run a command"}}},
		configTestTool{Base: tools.Base{Metadata: tools.Metadata{Name: "npm", LaunchHelp: "Launch npm"}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = BuildConfiguredLaunch(Params{Registry: registry, Paths: config.Paths{Home: home}}, configPath, nil)
	if err == nil || !strings.Contains(err.Error(), "tools.npm.params is only supported on the primary tool") {
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
projects:
  foo:
tools:
  exec:
`))

	_, err := loadLaunchConfigWithPaths(configPath, config.Paths{Home: home})
	if err == nil || !strings.Contains(err.Error(), "settings.suppressWarnings[0]") {
		t.Fatalf("error = %v", err)
	}
}

func TestBuildConfiguredLaunchRejectsUnknownTools(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, "toby.yaml")
	writeTestFile(t, configPath, []byte(`
projects:
  foo:
tools:
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
	writeTestFile(t, filepath.Join(project, projectLaunchConfigName), []byte("projects: {}\ntools: {}\n"))
	cfgSvc, err := appconfig.Load(t.TempDir(), home)
	if err != nil {
		t.Fatal(err)
	}
	parsed := DirectLaunch{Options: tools.Options{Env: "app"}, RequestedTools: []string{"opencode"}}
	var stderr bytes.Buffer
	_, ok, err := MaybeAutoloadProjectConfig(Params{Paths: configPaths(home, projectRoot), Config: cfgSvc, Stderr: &stderr}, parsed, "opencode")
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
projects:
  sibling:
tools:
  opencode:
  npm:
`))
	configDir := t.TempDir()
	writeTestFile(t, filepath.Join(configDir, "config.yaml"), []byte(`
settings:
  autoloadProjectConfig: true
`))
	cfgSvc, err := appconfig.Load(configDir, home)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := tools.NewRegistry([]tools.Tool{
		configTestTool{Base: tools.Base{Metadata: tools.Metadata{Name: "opencode", LaunchHelp: "Launch OpenCode"}}},
		configTestTool{Base: tools.Base{Metadata: tools.Metadata{Name: "npm", LaunchHelp: "Launch npm"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed := DirectLaunch{Options: tools.Options{Env: "app"}, RequestedTools: []string{"opencode"}}
	launch, ok, err := MaybeAutoloadProjectConfig(Params{Registry: registry, Paths: configPaths(home, projectRoot), Config: cfgSvc}, parsed, "opencode")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected autoload")
	}
	if launch.Options.Env != "review" || launch.Primary != "opencode" {
		t.Fatalf("launch = %#v", launch)
	}
	wantTools := []string{"opencode", "npm"}
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

func TestLaunchConfigDecodesContainerPorts(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, "toby.yaml")
	writeTestFile(t, configPath, []byte(`
container:
  image: custom-node
  ports:
    - "8080:3000"
    - " 127.0.0.1:9090:9090/udp "
    - ""
projects:
  demo:
tools:
  opencode:
`))
	cfg, err := loadLaunchConfigWithPaths(configPath, config.Paths{Home: home, ProjectRoot: filepath.Join(home, "Projects")})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.Container.Ports, []string{"8080:3000", "127.0.0.1:9090:9090/udp"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ports = %#v, want %#v", got, want)
	}
	if overrides := overridesFromLaunchConfig(cfg); !reflect.DeepEqual(overrides.Ports, cfg.Container.Ports) {
		t.Fatalf("override ports = %#v, want %#v", overrides.Ports, cfg.Container.Ports)
	}
}

func TestLoadLaunchConfigRejectsLegacyProjectAndToolKeys(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, "toby.yaml")
	// Pre-rename singular keys (`project`/`tool`) are no longer accepted.
	writeTestFile(t, configPath, []byte("project:\n  foo:\ntool:\n  opencode:\n"))
	if _, err := loadLaunchConfigWithPaths(configPath, config.Paths{Home: home, ProjectRoot: filepath.Join(home, "Projects")}); err == nil {
		t.Fatal("expected legacy project/tool keys to be rejected")
	}
}

func TestMergeLaunchOverridesAppendsPorts(t *testing.T) {
	dst := appconfig.LaunchOverrides{Ports: []string{"8080:3000"}}
	mergeLaunchOverrides(&dst, appconfig.LaunchOverrides{Ports: []string{"9090:9090"}})
	if got, want := dst.Ports, []string{"8080:3000", "9090:9090"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("merged ports = %#v, want %#v", got, want)
	}
}

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
