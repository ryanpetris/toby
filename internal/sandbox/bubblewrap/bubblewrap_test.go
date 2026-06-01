//go:build !darwin

package bubblewrap

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/platform/executil"
	"petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/tools/helpers"
	"petris.dev/toby/internal/tools/tool"
)

type fakeRunner struct{}

func (fakeRunner) Run(context.Context, []string, map[string]string, executil.Options) (int, error) {
	return 0, nil
}

type recordingRunner struct {
	argv []string
	env  map[string]string
}

func (r *recordingRunner) Run(_ context.Context, argv []string, env map[string]string, _ executil.Options) (int, error) {
	r.argv = append([]string(nil), argv...)
	r.env = env
	return 0, nil
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
	sbx, err := factory.FromOptions(&tool.CommandOptions{Env: "demo", SandboxRuntime: sandbox.RuntimeBubblewrap})
	if err != nil {
		t.Fatal(err)
	}
	regularSandboxPath := filepath.Join(paths.Home, ".config", "regular")
	readonlySandboxPath := filepath.Join(paths.Home, ".config", "readonly")
	devSandboxPath := "/var/run/demo.sock"
	binds := []tool.Bind{
		{HostPath: "/host/regular", Target: helpers.HomeTarget(".config", "regular"), Type: tool.BindRegular},
		{HostPath: "/host/readonly", Target: helpers.HomeTarget(".config", "readonly"), Type: tool.BindReadOnly, Optional: true},
		{HostPath: "/host/demo.sock", Target: helpers.AbsoluteTarget(devSandboxPath), Type: tool.BindDev, Optional: true},
	}
	cmd, err := sbx.(*instance).BuildCommand(sandbox.RunSpec{Argv: []string{"/bin/true"}, Binds: binds, Env: tool.Environment{"TOBY_CONTROL_HOST": "127.0.0.1:1234", "TOBY_CONTROL_TOKEN": "secret", "HOME": paths.Home}})
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
	assertContainsSequence(t, cmd, []string{"--setenv", "TOBY_CONTROL_HOST", "127.0.0.1:1234"})
	assertContainsSequence(t, cmd, []string{"--setenv", "TOBY_CONTROL_TOKEN", "secret"})
	assertContainsSequence(t, cmd, []string{"--setenv", "HOME", paths.Home})
	assertContainsSequence(t, cmd, []string{"--bind", filepath.Join(paths.SandboxRoot, "demo"), paths.Home})
	assertContainsSequence(t, cmd, []string{"--bind", filepath.Join(runtimeDir, "toby"), filepath.Join(runtimeDir, "toby")})
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
		SandboxRuntime: sandbox.RuntimeBubblewrap,
		Workdir:        "/tmp/custom-workdir",
		Projects: []tool.ProjectMount{
			{Name: "foo", Source: firstSource},
			{Name: "baz", Source: secondSource},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := sbx.(*instance).BuildCommand(sandbox.RunSpec{Argv: []string{"/bin/true"}})
	if err != nil {
		t.Fatal(err)
	}
	firstTarget := filepath.Join(paths.ProjectRoot, "foo")
	secondTarget := filepath.Join(paths.ProjectRoot, "baz")
	assertContainsSequence(t, cmd, []string{"--bind", firstSource, firstTarget})
	assertContainsSequence(t, cmd, []string{"--bind", secondSource, secondTarget})
	assertContainsSequence(t, cmd, []string{"--chdir", "/tmp/custom-workdir"})
}

func TestRunDoesNotReplaceBubblewrapHostEnvironment(t *testing.T) {
	home := t.TempDir()
	paths := testPaths(home)
	projectDir := filepath.Join(paths.ProjectRoot, "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{}
	factory := testFactory(paths, runner)
	sbx, err := factory.FromOptions(&tool.CommandOptions{Env: "demo", SandboxRuntime: sandbox.RuntimeBubblewrap})
	if err != nil {
		t.Fatal(err)
	}
	code, err := sbx.Run(context.Background(), sandbox.RunSpec{Argv: []string{"/bin/true"}, Env: tool.Environment{"TOBY_CONTROL_HOST": "127.0.0.1:1234"}})
	if err != nil || code != 0 {
		t.Fatalf("Run = %d, %v", code, err)
	}
	if runner.env != nil {
		t.Fatalf("bubblewrap runner env = %#v, want host env", runner.env)
	}
	assertContainsSequence(t, runner.argv, []string{"--setenv", "TOBY_CONTROL_HOST", "127.0.0.1:1234"})
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
		SandboxRuntime: sandbox.RuntimeBubblewrap,
		Projects:       []tool.ProjectMount{{Name: "foo", Source: source}},
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := sbx.(*instance).BuildCommand(sandbox.RunSpec{Argv: []string{"/bin/true"}})
	if err != nil {
		t.Fatal(err)
	}
	assertContainsSequence(t, cmd, []string{"--chdir", filepath.Join(paths.ProjectRoot, "foo")})
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
	sbx, err := factory.FromOptions(&tool.CommandOptions{Env: "demo", SandboxRuntime: sandbox.RuntimeBubblewrap, BubblewrapRoot: customRoot})
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := sbx.(*instance).BuildCommand(sandbox.RunSpec{Argv: []string{"/bin/true"}})
	if err != nil {
		t.Fatal(err)
	}
	assertContainsSequence(t, cmd, []string{"--bind", filepath.Join(customRoot, "demo"), paths.Home})
}

func TestBubblewrapHelpers(t *testing.T) {
	t.Setenv("TOBY_TEST_ENV", "configured")
	if got := envString("TOBY_TEST_ENV", "fallback"); got != "configured" {
		t.Fatalf("envString configured = %q", got)
	}
	if got := envString("TOBY_TEST_MISSING", "fallback"); got != "fallback" {
		t.Fatalf("envString fallback = %q", got)
	}
	t.Setenv("XDG_RUNTIME_DIR", "/custom/runtime")
	if got, want := bubblewrapRuntimeDir("/home/demo"), "/custom/runtime"; got != want {
		t.Fatalf("bubblewrapRuntimeDir configured = %q, want %q", got, want)
	}
	t.Setenv("XDG_RUNTIME_DIR", "")
	if got, want := bubblewrapRuntimeDir("/home/demo"), filepath.Join("/run/user", strconv.Itoa(os.Getuid())); got != want {
		t.Fatalf("bubblewrapRuntimeDir fallback = %q, want %q", got, want)
	}
	sbx := &instance{runtime: "/run/user/1000"}
	if got, want := sbx.runtimeBind("wayland-0"), []string{"--ro-bind-try", filepath.Join("/run/user/1000", "wayland-0"), filepath.Join("/run/user/1000", "wayland-0")}; !slices.Equal(got, want) {
		t.Fatalf("runtimeBind = %#v, want %#v", got, want)
	}
	if got := (&instance{}).runtimeBind("wayland-0"); got != nil {
		t.Fatalf("empty runtimeBind = %#v", got)
	}
	if bindFlag(tool.BindRegular, false) != "--bind" || bindFlag(tool.BindReadOnly, true) != "--ro-bind-try" || bindFlag(tool.BindDev, true) != "--dev-bind-try" || bindFlag("unknown", false) != "--bind" {
		t.Fatal("unexpected bind flags")
	}
}

func testPaths(home string) config.Paths {
	return config.Paths{
		Home:        home,
		ProjectRoot: filepath.Join(home, "Projects"),
		SandboxRoot: filepath.Join(home, "Scratch", "Toby"),
	}
}

func testFactory(paths config.Paths, runner executil.Runner) sandbox.Factory {
	factory, err := sandbox.NewFactory(paths, []sandbox.Environment{
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

func assertNotContainsSequence(t *testing.T, values, sequence []string) {
	t.Helper()
	for i := 0; i+len(sequence) <= len(values); i++ {
		if slices.Equal(values[i:i+len(sequence)], sequence) {
			t.Fatalf("%#v contains sequence %#v", values, sequence)
		}
	}
}
