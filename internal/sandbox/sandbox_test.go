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

type bindTool struct {
	tool.Base
	binds []tool.Bind
}

func (t bindTool) Binds() []tool.Bind {
	return append([]tool.Bind(nil), t.binds...)
}

func TestBuildCommandBindsRuntimeSocketBinaryProjectAndToolBinds(t *testing.T) {
	home := t.TempDir()
	projectRoot := filepath.Join(home, "Projects")
	projectDir := filepath.Join(projectRoot, "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	paths := testPaths(home)
	paths.XAuthority = filepath.Join(home, ".Xauthority")
	factory := NewFactory(paths, fakeRunner{})
	sbx, err := factory.FromOptions(&tool.CommandOptions{Env: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	regularSandboxPath := filepath.Join(home, ".config", "regular")
	readonlySandboxPath := filepath.Join(home, ".config", "readonly")
	devSandboxPath := "/var/run/demo.sock"
	registry, err := tool.NewRegistry(tool.RegistryParams{Tools: []tool.Tool{bindTool{
		Base: tool.Base{Metadata: tool.Metadata{Name: "bind"}},
		binds: []tool.Bind{
			{HostPath: "/host/regular", SandboxPath: regularSandboxPath, Type: tool.BindRegular},
			{HostPath: "/host/readonly", SandboxPath: readonlySandboxPath, Type: tool.BindReadOnly, Optional: true},
			{HostPath: "/host/demo.sock", SandboxPath: devSandboxPath, Type: tool.BindDev, Optional: true},
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	toolset, err := registry.Build([]string{"bind"}, "")
	if err != nil {
		t.Fatal(err)
	}
	cmd := sbx.BuildCommand([]string{"/bin/true"}, sbx.CommandMounts(toolset, "/host/control.sock", "/host/toby"))
	assertContainsSequence(t, cmd, []string{"/usr/bin/bwrap", "--die-with-parent", "--unshare-pid"})
	assertContainsSequence(t, cmd, []string{"--dev-bind", "/dev", "/dev"})
	assertContainsSequence(t, cmd, []string{"--tmpfs", paths.XDGRuntimeDir})
	assertContainsSequence(t, cmd, []string{"--dir", filepath.Join(paths.XDGRuntimeDir, "toby")})
	assertContainsSequence(t, cmd, []string{"--dir", filepath.Join(paths.XDGRuntimeDir, "toby", "bin")})
	assertContainsSequence(t, cmd, []string{"--ro-bind", "/host/toby", filepath.Join(paths.XDGRuntimeDir, "toby", "bin", "toby")})
	assertContainsSequence(t, cmd, []string{"--bind", "/host/control.sock", filepath.Join(paths.XDGRuntimeDir, "toby", "sandbox.sock")})
	assertContainsSequence(t, cmd, []string{"--ro-bind-try", filepath.Join(paths.XDGRuntimeDir, "pulse"), filepath.Join(paths.XDGRuntimeDir, "pulse")})
	assertContainsSequence(t, cmd, []string{"--ro-bind-try", filepath.Join(paths.XDGRuntimeDir, "pipewire-test"), filepath.Join(paths.XDGRuntimeDir, "pipewire-test")})
	assertContainsSequence(t, cmd, []string{"--ro-bind-try", filepath.Join(paths.XDGRuntimeDir, "wayland-test"), filepath.Join(paths.XDGRuntimeDir, "wayland-test")})
	assertContainsSequence(t, cmd, []string{"--ro-bind-try", "/run/udev", "/run/udev"})
	assertContainsSequence(t, cmd, []string{"--ro-bind-try", paths.XAuthority, paths.XAuthority})
	assertContainsSequence(t, cmd, []string{"--bind", sbx.HomeDir(), home})
	assertContainsSequence(t, cmd, []string{"--bind", projectDir, projectDir})
	assertContainsSequence(t, cmd, []string{"--bind", "/usr/bin/true", "/usr/bin/xdg-open"})
	assertContainsSequence(t, cmd, []string{"--bind", "/host/regular", regularSandboxPath})
	assertContainsSequence(t, cmd, []string{"--ro-bind-try", "/host/readonly", readonlySandboxPath})
	assertContainsSequence(t, cmd, []string{"--dev-bind-try", "/host/demo.sock", devSandboxPath})
	assertContainsSequence(t, cmd, []string{"--chdir", projectDir})
	assertContainsSequence(t, cmd, []string{"/bin/true"})
	if slices.Contains(cmd, "/run/dbus") || slices.Contains(cmd, filepath.Join(paths.XDGRuntimeDir, "bus")) {
		t.Fatalf("command unexpectedly includes dbus bindings: %#v", cmd)
	}
}

func TestProjectOutsideHomeRejected(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	paths := testPaths(home)
	factory := NewFactory(paths, fakeRunner{})
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
	factory := NewFactory(testPaths(home), fakeRunner{})
	sbx, err := factory.FromOptions(&tool.CommandOptions{Env: "demo", Project: projectDir})
	if err != nil {
		t.Fatal(err)
	}
	if sbx.projectDir != projectDir {
		t.Fatalf("projectDir = %q, want %q", sbx.projectDir, projectDir)
	}
}

func TestOpenCodeConfigDirUsesSandboxRoot(t *testing.T) {
	home := t.TempDir()
	paths := testPaths(home)
	if err := os.MkdirAll(filepath.Join(paths.ProjectRoot, "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	factory := NewFactory(paths, fakeRunner{})
	sbx, err := factory.FromOptions(&tool.CommandOptions{Env: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(paths.SandboxRoot, ".config", "opencode")
	if sbx.OpenCodeConfigDir() != want {
		t.Fatalf("OpenCodeConfigDir = %q, want %q", sbx.OpenCodeConfigDir(), want)
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
	factory := NewFactory(paths, fakeRunner{})
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
	factory := NewFactory(paths, fakeRunner{})
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
	factory := NewFactory(paths, fakeRunner{})
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
	factory := NewFactory(paths, fakeRunner{})
	sbx, err := factory.FromOptions(&tool.CommandOptions{Env: "foobar"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sbx.VisibleHostPath("foobar/link"); err == nil {
		t.Fatal("expected symlink escape to be rejected")
	}
}

func TestSetupContextPrependsTobyBinAndSetsRuntimeDir(t *testing.T) {
	home := t.TempDir()
	sbx := &Sandbox{paths: testPaths(home), label: "demo"}
	run := &tool.RunContext{Toolset: &tool.Toolset{}, Env: tool.Environment{"PATH": "/usr/bin"}}
	sbx.SetupContext(run)
	pathEntries := strings.Split(run.Env["PATH"], ":")
	want := []string{filepath.Join(home, "runtime", "toby", "bin"), filepath.Join(home, ".local", "bin"), "/usr/bin"}
	if !slices.Equal(pathEntries, want) {
		t.Fatalf("PATH entries = %#v, want %#v", pathEntries, want)
	}
	if run.Env["XDG_RUNTIME_DIR"] != filepath.Join(home, "runtime") {
		t.Fatalf("XDG_RUNTIME_DIR = %q", run.Env["XDG_RUNTIME_DIR"])
	}
	if run.Env["TOBY_SANDBOX"] != "1" {
		t.Fatalf("TOBY_SANDBOX = %q", run.Env["TOBY_SANDBOX"])
	}
	if sbx.TobyContextDir() != filepath.Join(home, "runtime", "toby", "context") {
		t.Fatalf("TobyContextDir = %q", sbx.TobyContextDir())
	}
	if sbx.TobyGitAgentsPath() != filepath.Join(home, "runtime", "toby", "context", "GIT_AGENTS.md") {
		t.Fatalf("TobyGitAgentsPath = %q", sbx.TobyGitAgentsPath())
	}
}

func testPaths(home string) config.Paths {
	return config.Paths{
		Home:           home,
		ProjectRoot:    filepath.Join(home, "Projects"),
		SandboxRoot:    filepath.Join(home, "Scratch", "Toby"),
		XDGRuntimeDir:  filepath.Join(home, "runtime"),
		PipewireCore:   "pipewire-test",
		WaylandDisplay: "wayland-test",
	}
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
