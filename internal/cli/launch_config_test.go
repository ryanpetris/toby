package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/tool"
)

func TestLoadLaunchConfigDefaultsSandboxNameAndResolvesProjectPaths(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "configs", "app")
	absolute := filepath.Join(home, "absolute")
	configPath := filepath.Join(dir, "toby.yaml")
	writeTestFile(t, configPath, []byte(`
sandbox:
  autoUpgrade: true
  runtime: docker
  docker:
    image: custom-node
    home: /home/custom
    projects: /workspace/custom
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
	if cfg.Sandbox.Name != "foo" || !cfg.Sandbox.AutoUpgrade {
		t.Fatalf("sandbox = %#v", cfg.Sandbox)
	}
	wantWorkdir := "~/literal-workdir/../raw"
	if cfg.Workdir != wantWorkdir {
		t.Fatalf("workdir = %q", cfg.Workdir)
	}
	if cfg.Sandbox.Runtime != "docker" || cfg.Sandbox.Docker.Image != "custom-node" || cfg.Sandbox.Docker.Home != "/home/custom" || cfg.Sandbox.Docker.Projects != "/workspace/custom" {
		t.Fatalf("sandbox docker config = %#v", cfg.Sandbox)
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
	writeTestFile(t, configPath, []byte(`{"sandbox":{"name":"json-env"},"projects":["foo"],"tools":["opencode"]}`))

	cfg, err := loadLaunchConfig(configPath, home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sandbox.Name != "json-env" {
		t.Fatalf("sandbox name = %q", cfg.Sandbox.Name)
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
	if launch.Options.Env != "foo" || launch.Options.Workdir != "/tmp/work" || len(launch.Options.Projects) != 1 || launch.Options.Projects[0].Name != "foo" {
		t.Fatalf("options = %#v", launch.Options)
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
