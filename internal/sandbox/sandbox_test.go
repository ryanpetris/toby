package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/executil"
	"petris.dev/toby/internal/tool"
)

type fakeRunner struct{}

func (fakeRunner) Run(context.Context, []string, map[string]string, executil.Options) (int, error) {
	return 0, nil
}

type recordingRunner struct {
	commands [][]string
}

func (r *recordingRunner) Run(_ context.Context, argv []string, _ map[string]string, _ executil.Options) (int, error) {
	r.commands = append(r.commands, append([]string(nil), argv...))
	return 0, nil
}

type bindTool struct {
	tool.Base
	binds []tool.Bind
}

func (t bindTool) Binds() []tool.Bind {
	return append([]tool.Bind(nil), t.binds...)
}

func TestBuildCommandBindsProjectAndToolBinds(t *testing.T) {
	home := t.TempDir()
	projectRoot := filepath.Join(home, "Projects")
	projectDir := filepath.Join(projectRoot, "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	paths := testPaths(home)
	runtimeDir := filepath.Join(home, "runtime")
	xauthority := filepath.Join(home, ".Xauthority")
	factory := testFactory(paths, fakeRunner{})
	sbx, err := factory.FromOptions(&tool.CommandOptions{Env: "demo", SandboxRuntime: RuntimeBubblewrap})
	if err != nil {
		t.Fatal(err)
	}
	regularSandboxPath := filepath.Join(home, ".config", "regular")
	readonlySandboxPath := filepath.Join(home, ".config", "readonly")
	devSandboxPath := "/var/run/demo.sock"
	registry, err := tool.NewRegistry(tool.RegistryParams{Tools: []tool.Tool{bindTool{
		Base: tool.Base{Metadata: tool.Metadata{Name: "bind"}},
		binds: []tool.Bind{
			{HostPath: "/host/regular", Target: tool.HomeTarget(".config", "regular"), Type: tool.BindRegular},
			{HostPath: "/host/readonly", Target: tool.HomeTarget(".config", "readonly"), Type: tool.BindReadOnly, Optional: true},
			{HostPath: "/host/demo.sock", Target: tool.AbsoluteTarget(devSandboxPath), Type: tool.BindDev, Optional: true},
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	toolset, err := registry.Build([]string{"bind"}, "")
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := sbx.(*BubblewrapInstance).BuildCommand(RunSpec{Argv: []string{"/bin/true"}, Toolset: toolset})
	if err != nil {
		t.Fatal(err)
	}
	assertContainsSequence(t, cmd, []string{"/usr/bin/bwrap", "--die-with-parent", "--unshare-pid"})
	assertContainsSequence(t, cmd, []string{"--dev-bind", "/dev", "/dev"})
	assertContainsSequence(t, cmd, []string{"--ro-bind-try", filepath.Join(runtimeDir, "pulse"), filepath.Join(runtimeDir, "pulse")})
	assertContainsSequence(t, cmd, []string{"--ro-bind-try", filepath.Join(runtimeDir, "pipewire-test"), filepath.Join(runtimeDir, "pipewire-test")})
	assertContainsSequence(t, cmd, []string{"--ro-bind-try", filepath.Join(runtimeDir, "wayland-test"), filepath.Join(runtimeDir, "wayland-test")})
	assertContainsSequence(t, cmd, []string{"--ro-bind-try", "/run/udev", "/run/udev"})
	assertContainsSequence(t, cmd, []string{"--ro-bind-try", xauthority, xauthority})
	assertContainsSequence(t, cmd, []string{"--bind", filepath.Join(paths.SandboxRoot, "demo"), home})
	assertContainsSequence(t, cmd, []string{"--bind", projectDir, projectDir})
	assertContainsSequence(t, cmd, []string{"--bind", "/usr/bin/true", "/usr/bin/xdg-open"})
	assertContainsSequence(t, cmd, []string{"--bind", "/host/regular", regularSandboxPath})
	assertContainsSequence(t, cmd, []string{"--ro-bind-try", "/host/readonly", readonlySandboxPath})
	assertContainsSequence(t, cmd, []string{"--dev-bind-try", "/host/demo.sock", devSandboxPath})
	assertContainsSequence(t, cmd, []string{"--chdir", projectDir})
	assertContainsSequence(t, cmd, []string{"/bin/true"})
	if slices.Contains(cmd, "/run/dbus") || slices.Contains(cmd, filepath.Join(runtimeDir, "bus")) {
		t.Fatalf("command unexpectedly includes dbus bindings: %#v", cmd)
	}
}

func TestProjectOutsideHomeRejected(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	paths := testPaths(home)
	factory := testFactory(paths, fakeRunner{})
	_, err := factory.FromOptions(&tool.CommandOptions{Env: "demo", Project: outside})
	if err == nil {
		t.Fatal("expected project outside home to be rejected")
	}
}

func TestProjectUnderProjectRootAccepted(t *testing.T) {
	home := t.TempDir()
	projectDir := filepath.Join(home, "Projects", "src", "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	factory := testFactory(testPaths(home), fakeRunner{})
	sbx, err := factory.FromOptions(&tool.CommandOptions{Env: "demo", Project: projectDir})
	if err != nil {
		t.Fatal(err)
	}
	visible, ok := sbx.ProjectPath("demo")
	if !ok || visible != filepath.Join(home, "Projects", "demo") {
		t.Fatalf("project path = %q, %v", visible, ok)
	}
	hostPath, err := sbx.VisibleHostPath("demo")
	if err != nil {
		t.Fatal(err)
	}
	if hostPath != projectDir {
		t.Fatalf("visible host path = %q, want %q", hostPath, projectDir)
	}
}

func TestConfiguredProjectsMountUnderProjectRootByName(t *testing.T) {
	home := t.TempDir()
	paths := testPaths(home)
	firstSource := filepath.Join(home, "sources", "bar")
	secondSource := filepath.Join(t.TempDir(), "external")
	if err := os.MkdirAll(firstSource, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(secondSource, 0o755); err != nil {
		t.Fatal(err)
	}
	factory := testFactory(paths, fakeRunner{})
	sbx, err := factory.FromOptions(&tool.CommandOptions{
		Env:            "env",
		SandboxRuntime: RuntimeBubblewrap,
		Workdir:        "/tmp/custom-workdir",
		Projects: []tool.ProjectMount{
			{Name: "foo", Source: firstSource},
			{Name: "baz", Source: secondSource},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := sbx.(*BubblewrapInstance).BuildCommand(RunSpec{Argv: []string{"/bin/true"}})
	if err != nil {
		t.Fatal(err)
	}
	firstTarget := filepath.Join(paths.ProjectRoot, "foo")
	secondTarget := filepath.Join(paths.ProjectRoot, "baz")
	assertContainsSequence(t, cmd, []string{"--bind", firstSource, firstTarget})
	assertContainsSequence(t, cmd, []string{"--bind", secondSource, secondTarget})
	assertContainsSequence(t, cmd, []string{"--chdir", "/tmp/custom-workdir"})
}

func TestConfiguredProjectsDefaultWorkdirIsPrimaryProject(t *testing.T) {
	home := t.TempDir()
	paths := testPaths(home)
	source := filepath.Join(home, "sources", "bar")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	factory := testFactory(paths, fakeRunner{})
	sbx, err := factory.FromOptions(&tool.CommandOptions{
		Env:            "env",
		SandboxRuntime: RuntimeBubblewrap,
		Projects:       []tool.ProjectMount{{Name: "foo", Source: source}},
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := sbx.(*BubblewrapInstance).BuildCommand(RunSpec{Argv: []string{"/bin/true"}})
	if err != nil {
		t.Fatal(err)
	}
	assertContainsSequence(t, cmd, []string{"--chdir", filepath.Join(paths.ProjectRoot, "foo")})
}

func TestConfiguredProjectVisibleHostPathUsesProjectName(t *testing.T) {
	home := t.TempDir()
	paths := testPaths(home)
	source := filepath.Join(t.TempDir(), "source")
	nested := filepath.Join(source, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	factory := testFactory(paths, fakeRunner{})
	sbx, err := factory.FromOptions(&tool.CommandOptions{
		Env:      "env",
		Projects: []tool.ProjectMount{{Name: "baz", Source: source}},
	})
	if err != nil {
		t.Fatal(err)
	}
	visible, err := sbx.VisibleHostPath("baz/nested")
	if err != nil {
		t.Fatal(err)
	}
	if visible != nested {
		t.Fatalf("visible path = %q, want %q", visible, nested)
	}
	if _, err := sbx.VisibleHostPath("source/nested"); err == nil {
		t.Fatal("expected source path name to be invisible")
	}
}

func TestVisibleHostPathAllowsNestedRepositoryUnderVisibleProject(t *testing.T) {
	home := t.TempDir()
	paths := testPaths(home)
	project := filepath.Join(paths.ProjectRoot, "foobar")
	nested := filepath.Join(project, "baz", "bat")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	factory := testFactory(paths, fakeRunner{})
	sbx, err := factory.FromOptions(&tool.CommandOptions{Env: "foobar"})
	if err != nil {
		t.Fatal(err)
	}
	visible, err := sbx.VisibleHostPath("foobar/baz/bat")
	if err != nil {
		t.Fatal(err)
	}
	if visible != nested {
		t.Fatalf("visible path = %q, want %q", visible, nested)
	}
}

func TestVisibleHostPathRejectsDotSegmentRepository(t *testing.T) {
	home := t.TempDir()
	paths := testPaths(home)
	project := filepath.Join(paths.ProjectRoot, "foobar")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	factory := testFactory(paths, fakeRunner{})
	sbx, err := factory.FromOptions(&tool.CommandOptions{Env: "foobar"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sbx.VisibleHostPath("foobar/../baz"); err == nil {
		t.Fatal("expected dot segment repository to be rejected")
	}
}

func TestVisibleHostPathRejectsInvisibleRepository(t *testing.T) {
	home := t.TempDir()
	paths := testPaths(home)
	project := filepath.Join(paths.ProjectRoot, "foobar")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	factory := testFactory(paths, fakeRunner{})
	sbx, err := factory.FromOptions(&tool.CommandOptions{Env: "foobar"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sbx.VisibleHostPath("other"); err == nil {
		t.Fatal("expected invisible repository to be rejected")
	}
}

func TestVisibleHostPathRejectsSymlinkEscape(t *testing.T) {
	home := t.TempDir()
	paths := testPaths(home)
	project := filepath.Join(paths.ProjectRoot, "foobar")
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(project, "link")); err != nil {
		t.Fatal(err)
	}
	factory := testFactory(paths, fakeRunner{})
	sbx, err := factory.FromOptions(&tool.CommandOptions{Env: "foobar"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sbx.VisibleHostPath("foobar/link"); err == nil {
		t.Fatal("expected symlink escape to be rejected")
	}
}

func TestSetupContextPrependsTobyBinAndUsesFixedRuntimeDir(t *testing.T) {
	home := t.TempDir()
	paths := testPaths(home)
	sbx := &BubblewrapInstance{baseInstance: baseInstance{paths: paths, label: "demo", homeDir: home, projectsDir: paths.ProjectRoot, runtimeDir: RuntimeDir}}
	run := &tool.RunContext{Toolset: &tool.Toolset{}, Env: tool.Environment{"PATH": "/usr/bin", "XDG_RUNTIME_DIR": "/keep"}}
	sbx.SetupContext(run)
	pathEntries := strings.Split(run.Env["PATH"], ":")
	want := []string{filepath.Join(RuntimeDir, "bin"), filepath.Join(home, ".local", "bin"), "/usr/bin"}
	if !slices.Equal(pathEntries, want) {
		t.Fatalf("PATH entries = %#v, want %#v", pathEntries, want)
	}
	if run.Env["XDG_RUNTIME_DIR"] != "/keep" {
		t.Fatalf("XDG_RUNTIME_DIR = %q", run.Env["XDG_RUNTIME_DIR"])
	}
	if run.Env["TOBY_SANDBOX"] != "1" {
		t.Fatalf("TOBY_SANDBOX = %q", run.Env["TOBY_SANDBOX"])
	}
	if sbx.TobyContextDir() != filepath.Join(RuntimeDir, "context") {
		t.Fatalf("TobyContextDir = %q", sbx.TobyContextDir())
	}
	if sbx.TobyGitAgentsPath() != filepath.Join(RuntimeDir, "context", "GIT_AGENTS.md") {
		t.Fatalf("TobyGitAgentsPath = %q", sbx.TobyGitAgentsPath())
	}
}

func TestDockerBuildCommandMountsHomeProjectsAndUsesDefaultImage(t *testing.T) {
	home := t.TempDir()
	paths := testPaths(home)
	projectDir := filepath.Join(paths.ProjectRoot, "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	factory := testFactory(paths, fakeRunner{})
	sbx, err := factory.FromOptions(&tool.CommandOptions{Env: "demo", SandboxRuntime: RuntimeDocker})
	if err != nil {
		t.Fatal(err)
	}
	docker := sbx.(*DockerInstance)
	env := tool.Environment{"HOME": docker.HomeDir(), "XDG_PROJECTS_DIR": docker.Projects()}
	cmd, err := docker.BuildCommand(RunSpec{Argv: []string{docker.TobyBinaryPath(), "sandbox", "manager"}, Env: env})
	if err != nil {
		t.Fatal(err)
	}
	assertContainsSequence(t, cmd, []string{"docker", "run", "--rm", "--init", "-i"})
	assertContainsSequence(t, cmd, []string{"--network", "host"})
	assertContainsSequence(t, cmd, []string{"--mount", dockerVolume("toby-home-demo", paths.Home)})
	assertContainsSequence(t, cmd, []string{"--mount", dockerBind(projectDir, filepath.Join(paths.ProjectRoot, "demo"), false)})
	assertContainsSequence(t, cmd, []string{"--env", "HOME=" + paths.Home})
	assertContainsSequence(t, cmd, []string{"--env", "XDG_PROJECTS_DIR=" + paths.ProjectRoot})
	assertContainsSequence(t, cmd, []string{"--workdir", filepath.Join(paths.ProjectRoot, "demo"), DefaultDockerImage})
	assertContainsSequence(t, cmd, []string{docker.TobyBinaryPath(), "sandbox", "manager"})
	initCmd := docker.BuildHomeVolumeInitCommand()
	assertContainsSequence(t, initCmd, []string{"docker", "run", "--rm", "--user", "0:0", "--entrypoint", "sh"})
	assertContainsSequence(t, initCmd, []string{"--mount", dockerVolume("toby-home-demo", paths.Home)})
	assertContainsSequence(t, initCmd, []string{"--env", "HOME=" + paths.Home})
	assertContainsSequence(t, initCmd, []string{DefaultDockerImage, "-c", `set -e; mkdir -p "$1" "$1/.local/bin" "$1/.local/share" "$1/.cache" "$1/.config"; chown -R "$2:$3" "$1" 2>/dev/null || true; chmod -R u+rwX,go+rwX "$1"`, "sh", paths.Home})
}

func TestFactoryResolvesRelativeToolStateRootFromPrimaryProject(t *testing.T) {
	home := t.TempDir()
	paths := testPaths(home)
	projectDir := filepath.Join(paths.ProjectRoot, "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	factory := testFactory(paths, fakeRunner{})
	opts := &tool.CommandOptions{
		Env:            "demo",
		SandboxRuntime: RuntimeBubblewrap,
		ToolStates: tool.ToolStateSettings{Default: tool.ToolStateConfig{
			State:     tool.ToolStateHost,
			StateRoot: "state/root",
		}},
	}
	if _, err := factory.FromOptions(opts); err != nil {
		t.Fatal(err)
	}
	if got, want := opts.ToolStates.StateRootFor(tool.OpenCodeToolName), filepath.Join(projectDir, "state", "root"); got != want {
		t.Fatalf("state root = %q, want %q", got, want)
	}
}

func TestBubblewrapRootOptionOverridesDefaultSandboxRoot(t *testing.T) {
	home := t.TempDir()
	paths := testPaths(home)
	projectDir := filepath.Join(paths.ProjectRoot, "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	customRoot := filepath.Join(home, "CustomSandboxes")
	factory := testFactory(paths, fakeRunner{})
	sbx, err := factory.FromOptions(&tool.CommandOptions{Env: "demo", SandboxRuntime: RuntimeBubblewrap, BubblewrapRoot: customRoot})
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := sbx.(*BubblewrapInstance).BuildCommand(RunSpec{Argv: []string{"/bin/true"}})
	if err != nil {
		t.Fatal(err)
	}
	assertContainsSequence(t, cmd, []string{"--bind", filepath.Join(customRoot, "demo"), paths.Home})
}

func TestDockerRunInitializesHomeVolumeBeforeManager(t *testing.T) {
	home := t.TempDir()
	paths := testPaths(home)
	projectDir := filepath.Join(paths.ProjectRoot, "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{}
	factory := testFactory(paths, runner)
	sbx, err := factory.FromOptions(&tool.CommandOptions{Env: "demo", SandboxRuntime: RuntimeDocker})
	if err != nil {
		t.Fatal(err)
	}
	docker := sbx.(*DockerInstance)
	code, err := docker.Run(context.Background(), RunSpec{Argv: []string{docker.TobyBinaryPath(), "sandbox", "manager"}, Env: tool.Environment{}})
	if err != nil || code != 0 {
		t.Fatalf("Run = %d, %v", code, err)
	}
	if len(runner.commands) != 2 {
		t.Fatalf("commands = %#v", runner.commands)
	}
	assertContainsSequence(t, runner.commands[0], []string{"--entrypoint", "sh"})
	assertContainsSequence(t, runner.commands[0], []string{"--mount", dockerVolume("toby-home-demo", paths.Home)})
	assertContainsSequence(t, runner.commands[1], []string{"docker", "run", "--rm", "--init", "-i"})
}

func TestDockerOptionsOverrideHomeProjectsAndImage(t *testing.T) {
	home := t.TempDir()
	paths := testPaths(home)
	projectDir := filepath.Join(paths.ProjectRoot, "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	factory := testFactory(paths, fakeRunner{})
	sbx, err := factory.FromOptions(&tool.CommandOptions{
		Env:            "demo",
		SandboxRuntime: RuntimeDocker,
		DockerImage:    "custom:latest",
		DockerHome:     "/home/custom",
		DockerProjects: "~/workspace",
	})
	if err != nil {
		t.Fatal(err)
	}
	docker := sbx.(*DockerInstance)
	if docker.HomeDir() != "/home/custom" {
		t.Fatalf("HomeDir = %q", docker.HomeDir())
	}
	if docker.Projects() != "/home/custom/workspace" {
		t.Fatalf("Projects = %q", docker.Projects())
	}
	cmd, err := docker.BuildCommand(RunSpec{Argv: []string{"true"}, Env: tool.Environment{}})
	if err != nil {
		t.Fatal(err)
	}
	assertContainsSequence(t, cmd, []string{"--mount", dockerVolume("toby-home-demo", "/home/custom")})
	assertContainsSequence(t, cmd, []string{"--workdir", "/home/custom/workspace/demo", "custom:latest"})
}

func TestSandboxAndProjectNamesRejectSlashes(t *testing.T) {
	home := t.TempDir()
	paths := testPaths(home)
	projectDir := filepath.Join(paths.ProjectRoot, "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	factory := testFactory(paths, fakeRunner{})
	if _, err := factory.FromOptions(&tool.CommandOptions{Env: "team/demo"}); err == nil {
		t.Fatal("expected slash in sandbox name to be rejected")
	}
	if _, err := factory.FromOptions(&tool.CommandOptions{Projects: []tool.ProjectMount{{Name: "team/demo", Source: projectDir}}}); err == nil {
		t.Fatal("expected slash in project name to be rejected")
	}
}

func TestDockerHomeVolumeNameSanitizesLabel(t *testing.T) {
	if got, want := dockerHomeVolumeName("review env"), "toby-home-review-env"; got != want {
		t.Fatalf("volume name = %q, want %q", got, want)
	}
}

func testPaths(home string) config.Paths {
	return config.Paths{
		Home:        home,
		ProjectRoot: filepath.Join(home, "Projects"),
		SandboxRoot: filepath.Join(home, "Scratch", "Toby"),
	}
}

func testFactory(paths config.Paths, runner executil.Runner) Factory {
	factory, err := newFactory(paths, []Environment{
		newDockerEnvironment(paths, runner, "docker", nil),
		newBubblewrapEnvironment(paths, runner, "/usr/bin/bwrap", filepath.Join(paths.Home, "runtime"), "pipewire-test", "wayland-test", filepath.Join(paths.Home, ".Xauthority"), nil),
	})
	if err != nil {
		panic(err)
	}
	return factory
}

func assertContainsSequence(t *testing.T, values, sequence []string) {
	t.Helper()
	for i := 0; i+len(sequence) <= len(values); i++ {
		if slices.Equal(values[i:i+len(sequence)], sequence) {
			return
		}
	}
	t.Fatalf("%#v does not contain sequence %#v", values, sequence)
}
