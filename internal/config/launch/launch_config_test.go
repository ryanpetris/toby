package launchconfig

// Exercises project launch resolution, overlay precedence, and validation.

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"petris.dev/toby/internal/config"
	appconfig "petris.dev/toby/internal/config/app"
	"petris.dev/toby/internal/diagnostic/warning"
	"petris.dev/toby/internal/oci/image"
	"petris.dev/toby/internal/oci/imagesource"
	"petris.dev/toby/internal/tools"
)

func TestLoadLaunchConfigDefaultsNameAndResolvesProjectPaths(t *testing.T) {
	home := t.TempDir()
	projectRoot := filepath.Join(home, "Projects")
	dir := filepath.Join(home, "configs", "app")
	absolute := filepath.Join(home, "absolute")
	configPath := filepath.Join(dir, "toby.yaml")
	writeTestFile(
		t,
		configPath,
		[]byte(
			fullLaunchPrefixFixture+
				" "+
				absolute+
				fullLaunchSuffixFixture,
		),
	)

	cfg, err := loadLaunchConfigWithPaths(configPath, config.Paths{Home: home, ProjectRoot: projectRoot}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "" || !cfg.Settings.AutoUpgrade || cfg.Settings.Debug == nil || !*cfg.Settings.Debug || cfg.Settings.Yolo == nil || !*cfg.Settings.Yolo {
		t.Fatalf("settings/name = %#v %q", cfg.Settings, cfg.Name)
	}
	if cfg.Settings.Profile != "review" {
		t.Fatalf("profile = %q, want review", cfg.Settings.Profile)
	}
	wantWorkdir := "~/literal-workdir/../raw"
	if cfg.Workdir != wantWorkdir {
		t.Fatalf("workdir = %q", cfg.Workdir)
	}
	if cfg.Sandbox.Image != "custom-node" || cfg.Sandbox.Pull != image.PullAlways {
		t.Fatalf("sandbox config = %#v", cfg.Sandbox)
	}
	if !cfg.Settings.SuppressWarnings.Suppresses(warning.ProjectMissing) ||
		!cfg.Settings.SuppressWarnings.Suppresses(warning.PermissionAutoDeny) {
		t.Fatalf("suppress warnings = %#v", cfg.Settings.SuppressWarnings)
	}
	wantProjects := []tools.ProjectMount{
		{Name: "abs", Source: absolute},
		{Name: "bar", Source: dir + string(filepath.Separator) + "../bar-src"},
		{Name: "dot", Source: dir},
		{
			Name:               "foo",
			Source:             filepath.Join(projectRoot, "foo"),
			RequireProjectRoot: true,
		},
		{
			Name:               "named",
			Source:             filepath.Join(projectRoot, "named"),
			RequireProjectRoot: true,
		},
		{Name: "tilde", Source: home + "/tilde-source/../raw"},
	}
	if !reflect.DeepEqual(projectMounts(cfg.Projects), wantProjects) {
		t.Fatalf("projects = %#v, want %#v", cfg.Projects, wantProjects)
	}
	wantTools := []launchToolConfig{
		{Name: "npm", Label: "tools.npm"},
		{Name: "opencode", Label: "tools.opencode", Profile: "personal"},
		{Name: "uv", Label: "tools.uv"},
	}
	if !reflect.DeepEqual(cfg.Tools, wantTools) {
		t.Fatalf("tools = %#v, want %#v", cfg.Tools, wantTools)
	}
}

func TestLoadLaunchConfigParsesJSONWithYAMLParser(t *testing.T) {
	home := t.TempDir()
	projectRoot := filepath.Join(home, "Projects")
	configPath := filepath.Join(home, "toby.json")
	writeTestFile(t, configPath, []byte(`{"name":"json-env","sandbox":{"image":"custom-node"},"projects":{"foo":null},"tools":{"opencode":null}}`))

	cfg, err := loadLaunchConfigWithPaths(configPath, config.Paths{Home: home, ProjectRoot: projectRoot}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "json-env" {
		t.Fatalf("name = %q", cfg.Name)
	}
	if cfg.Sandbox.Image != "custom-node" {
		t.Fatalf("sandbox = %#v", cfg.Sandbox)
	}
	if got, want := cfg.Projects[0].Mount.Source, filepath.Join(projectRoot, "foo"); got != want {
		t.Fatalf("project source = %q, want %q", got, want)
	}
	if !cfg.Projects[0].Mount.RequireProjectRoot {
		t.Fatal("default project path must retain project-root provenance")
	}
}

func TestLoadLaunchConfigResolvesSandboxBuild(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "project", ".toby")
	configPath := filepath.Join(dir, "config.yaml")
	writeTestFile(
		t,
		configPath,
		[]byte(
			"sandbox:\n"+
				"  build:\n"+
				"    context: .\n"+
				"    dockerfile: Dockerfile\n",
		),
	)

	cfg, err := loadLaunchConfigWithPaths(
		configPath,
		config.Paths{Home: home},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	projectPath := filepath.Join(home, "project")
	if cfg.Sandbox.Source != imagesource.Build ||
		!cfg.Sandbox.SourceSet ||
		cfg.Sandbox.Build.Context != projectPath ||
		cfg.Sandbox.Build.Dockerfile != filepath.Join(
			projectPath,
			"Dockerfile",
		) ||
		cfg.Sandbox.Pull != image.PullIfMissing {
		t.Fatalf("sandbox = %#v", cfg.Sandbox)
	}
}

func TestLoadLaunchConfigAppliesPullPolicyToSandboxBuild(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, "project", ".toby", "config.yaml")
	writeTestFile(
		t,
		configPath,
		[]byte(
			"sandbox:\n"+
				"  build:\n"+
				"    context: .\n"+
				"  pull: always\n",
		),
	)

	cfg, err := loadLaunchConfigWithPaths(
		configPath,
		config.Paths{Home: home},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sandbox.Pull != image.PullAlways {
		t.Fatalf("pull policy = %q", cfg.Sandbox.Pull)
	}
}

func TestBuildConfiguredLaunchResolvesCommandNames(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, "toby.yaml")
	writeTestFile(t, configPath, []byte(nullableLaunchFixture))
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
	if launch.Overrides.Profile != "review" {
		t.Fatalf("profile = %q, want review", launch.Overrides.Profile)
	}
	if got, want := launch.Overrides.ToolProfiles, map[string]string{"github_cli": "personal"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tool profiles = %#v, want %#v", got, want)
	}
	if !launch.Overrides.SuppressWarnings.Suppresses(warning.PermissionAutoDeny) ||
		launch.Overrides.SuppressWarnings.Suppresses(warning.ProjectMissing) {
		t.Fatalf("suppress warnings = %#v", launch.Overrides.SuppressWarnings)
	}
	if got, want := launch.Extra, []string{"--repo", "x"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("extra = %#v, want %#v", got, want)
	}
}

func TestBuildConfiguredLaunchAppendsCLIArgsAfterPrimaryParams(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, "toby.yaml")
	writeTestFile(t, configPath, []byte(execPrimaryFixture))
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
	writeTestFile(t, configPath, []byte(customProjectsFixture))
	registry, err := tools.NewRegistry([]tools.Tool{
		configTestTool{Base: tools.Base{Metadata: tools.Metadata{Name: "opencode", LaunchHelp: "Launch OpenCode"}}},
		configTestTool{Base: tools.Base{Metadata: tools.Metadata{Name: "npm", LaunchHelp: "Launch npm"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed := DirectLaunch{Options: tools.Options{Env: "app"}, Extra: []string{"--foreground"}, RequestedTools: []string{"opencode"}}
	paths := config.Paths{Home: home, ProjectRoot: projectRoot}
	primaryProject, err := ResolveDirectLaunchProject(
		paths,
		parsed.Options,
		false,
	)
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
	wantProjects := []tools.ProjectMount{
		{
			Name:               "app",
			Source:             project,
			RequireProjectRoot: true,
		},
		{Name: "duplicate", Source: project},
		{Name: "extra", Source: extraProject},
		{
			Name:               "shared",
			Source:             sharedProject,
			RequireProjectRoot: true,
		},
	}
	if !reflect.DeepEqual(launch.Options.Projects, wantProjects) {
		t.Fatalf("projects = %#v, want %#v", launch.Options.Projects, wantProjects)
	}
}

func TestBuildOverlayConfiguredLaunchPreservesManagedTerminalOverride(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, "project-config.yaml")
	writeTestFile(t, configPath, []byte(projectNameFixture))
	registry, err := tools.NewRegistry([]tools.Tool{
		configTestTool{Base: tools.Base{Metadata: tools.Metadata{Name: "opencode", LaunchHelp: "Launch OpenCode"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	paths := config.Paths{
		Home:        home,
		ProjectRoot: filepath.Join(home, "Projects"),
	}
	primaryProject := tools.ProjectMount{
		Name:   "app",
		Source: filepath.Join(paths.ProjectRoot, "app"),
	}

	tests := []struct {
		name        string
		host        bool
		overrideSet bool
		override    bool
		want        bool
	}{
		{
			name:        "explicit true overrides false host",
			host:        false,
			overrideSet: true,
			override:    true,
			want:        true,
		},
		{
			name:        "explicit false overrides true host",
			host:        true,
			overrideSet: true,
			override:    false,
			want:        false,
		},
		{
			name: "absent remains absent",
			host: false,
			want: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			hostValue := "false"
			if test.host {
				hostValue = "true"
			}
			hostDir := t.TempDir()
			writeTestFile(
				t,
				filepath.Join(hostDir, "config.yaml"),
				[]byte("settings:\n  managedTerminal: "+hostValue+"\n"),
			)
			base, err := appconfig.Load(hostDir, home)
			if err != nil {
				t.Fatal(err)
			}

			var override *bool
			if test.overrideSet {
				value := test.override
				override = &value
			}
			parsed := DirectLaunch{
				Options: tools.Options{Env: "app"},
				Overrides: appconfig.LaunchOverrides{
					ManagedTerminal: override,
				},
				RequestedTools: []string{"opencode"},
			}
			launch, err := BuildOverlayConfiguredLaunch(
				Params{Registry: registry, Paths: paths, Config: base},
				configPath,
				parsed,
				"opencode",
				primaryProject,
			)
			if err != nil {
				t.Fatal(err)
			}

			if !test.overrideSet {
				if launch.Overrides.ManagedTerminal != nil {
					t.Fatalf(
						"managed terminal override = %v, want nil",
						*launch.Overrides.ManagedTerminal,
					)
				}
			} else {
				if launch.Overrides.ManagedTerminal == nil ||
					*launch.Overrides.ManagedTerminal != test.override {
					t.Fatalf(
						"managed terminal override = %#v, want %v",
						launch.Overrides.ManagedTerminal,
						test.override,
					)
				}

				*override = !test.override
				if *launch.Overrides.ManagedTerminal != test.override {
					t.Fatal("managed terminal override retained the source pointer")
				}
			}

			if got := base.WithOverrides(launch.Overrides).Settings().ManagedTerminalEnabled(); got != test.want {
				t.Fatalf("managed terminal enabled = %v, want %v", got, test.want)
			}
		})
	}
}

func TestBuildConfiguredLaunchRejectsParamsOnSecondaryTool(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, "toby.yaml")
	writeTestFile(t, configPath, []byte(secondaryToolParamsFixture))
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

func TestResolveDirectLaunchProjectUsesProjectRootForRelativePaths(
	t *testing.T,
) {
	home := t.TempDir()
	projectRoot := filepath.Join(home, "Projects")
	project := filepath.Join(projectRoot, "nested", "_temp")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	paths := config.Paths{Home: home, ProjectRoot: projectRoot}

	resolved, err := ResolveDirectLaunchProject(
		paths,
		tools.Options{Env: "shared-home", Project: "nested/_temp"},
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Name != "_temp" ||
		resolved.Source != project ||
		!resolved.RequireProjectRoot {
		t.Fatalf("resolved project = %#v", resolved)
	}
}

func TestResolveDirectLaunchProjectControlsOutsideRootPaths(t *testing.T) {
	home := t.TempDir()
	projectRoot := filepath.Join(home, "Projects")
	inside := filepath.Join(projectRoot, "inside")
	outside := filepath.Join(home, "outside")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	paths := config.Paths{Home: home, ProjectRoot: projectRoot}

	_, err := ResolveDirectLaunchProject(
		paths,
		tools.Options{Env: "home", Project: outside},
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "must be under") {
		t.Fatalf("outside-root error = %v", err)
	}

	resolved, err := ResolveDirectLaunchProject(
		paths,
		tools.Options{Env: "home", Project: outside},
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Source != outside || resolved.RequireProjectRoot {
		t.Fatalf("allowed outside project = %#v", resolved)
	}

	resolved, err = ResolveDirectLaunchProject(
		paths,
		tools.Options{Env: "home", Project: "../outside"},
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Source != outside || resolved.RequireProjectRoot {
		t.Fatalf("allowed relative outside project = %#v", resolved)
	}
}

func TestResolveDirectLaunchProjectRejectsSymlinkEscapeAndMissingDirectory(
	t *testing.T,
) {
	home := t.TempDir()
	projectRoot := filepath.Join(home, "Projects")
	outside := filepath.Join(home, "outside")
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(projectRoot, "linked")); err != nil {
		t.Fatal(err)
	}
	paths := config.Paths{Home: home, ProjectRoot: projectRoot}

	_, err := ResolveDirectLaunchProject(
		paths,
		tools.Options{Env: "home", Project: "linked"},
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "must be under") {
		t.Fatalf("symlink escape error = %v", err)
	}

	_, err = ResolveDirectLaunchProject(
		paths,
		tools.Options{Env: "home", Project: "missing"},
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing-directory error = %v", err)
	}
}

func TestLoadLaunchConfigIgnoresUnknownSuppressedWarning(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, "toby.yaml")
	writeTestFile(t, configPath, []byte(unknownWarningFixture))

	cfg, err := loadLaunchConfigWithPaths(configPath, config.Paths{Home: home}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Settings.UnknownSuppressWarnings) == 0 {
		t.Fatal("unknown suppressWarnings IDs were not collected")
	}
}

func TestBuildConfiguredLaunchRejectsUnknownTools(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, "toby.yaml")
	writeTestFile(t, configPath, []byte(unknownToolFixture))
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
	logger := &launchWarningLogger{}
	warnings := warning.NewService(logger, nil)
	_, ok, err := MaybeAutoloadProjectConfig(Params{Paths: configPaths(home, projectRoot), Config: cfgSvc, Warnings: warnings}, parsed, "opencode")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("autoload should be disabled")
	}
	if got := strings.Join(logger.messages, "\n"); !strings.Contains(got, "warning[project.autoload-disabled]") || !strings.Contains(got, projectLaunchConfigName) {
		t.Fatalf("warning = %q", got)
	}

	parsed.Options.Quiet = true
	logger.messages = nil
	_, ok, err = MaybeAutoloadProjectConfig(
		Params{
			Paths:    configPaths(home, projectRoot),
			Config:   cfgSvc,
			Warnings: warnings,
		},
		parsed,
		"opencode",
	)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("autoload should remain disabled in quiet mode")
	}
	if len(logger.messages) != 0 {
		t.Fatalf("quiet autoload warning = %q", logger.messages)
	}
}

type launchWarningLogger struct {
	messages []string
}

func (l *launchWarningLogger) Warn(message string, _ ...any) {
	l.messages = append(l.messages, message)
}

func (l *launchWarningLogger) WarnError(
	message string,
	_ error,
	_ ...any,
) {
	l.messages = append(l.messages, message)
}

func (l *launchWarningLogger) Error(message string, _ ...any) {
	l.messages = append(l.messages, message)
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
	writeTestFile(t, filepath.Join(project, projectLaunchConfigName), []byte(projectAutoloadFixture))
	configDir := t.TempDir()
	writeTestFile(t, filepath.Join(configDir, "config.yaml"), []byte(autoloadSettingFixture))
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
	wantProjects := []tools.ProjectMount{
		{
			Name:               "app",
			Source:             project,
			RequireProjectRoot: true,
		},
		{
			Name:               "sibling",
			Source:             sibling,
			RequireProjectRoot: true,
		},
	}
	if !reflect.DeepEqual(launch.Options.Projects, wantProjects) {
		t.Fatalf("projects = %#v, want %#v", launch.Options.Projects, wantProjects)
	}
}

func configPaths(home, projectRoot string) config.Paths {
	return config.Paths{Home: home, XDGConfigHome: filepath.Join(home, ".config"), ProjectRoot: projectRoot}
}

type configTestTool struct{ tools.Base }

func TestLaunchConfigDecodesSandboxPullPolicy(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, "toby.yaml")
	writeTestFile(t, configPath, []byte(sandboxImageFixture))
	cfg, err := loadLaunchConfigWithPaths(configPath, config.Paths{Home: home, ProjectRoot: filepath.Join(home, "Projects")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.Sandbox.Pull, image.PullNever; got != want {
		t.Fatalf("pull = %q, want %q", got, want)
	}
	if overrides := overridesFromLaunchConfig(cfg); overrides.Pull != cfg.Sandbox.Pull {
		t.Fatalf("override pull = %q, want %q", overrides.Pull, cfg.Sandbox.Pull)
	}
}

func TestMergeLaunchOverridesReplacesPullPolicy(t *testing.T) {
	dst := appconfig.LaunchOverrides{Pull: image.PullIfMissing}
	mergeLaunchOverrides(&dst, appconfig.LaunchOverrides{Pull: image.PullAlways})
	if got, want := dst.Pull, image.PullAlways; got != want {
		t.Fatalf("merged pull = %q, want %q", got, want)
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
