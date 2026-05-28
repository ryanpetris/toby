package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/executil"
	"petris.dev/toby/internal/tool"
)

type fakeRunner struct{}

func (fakeRunner) Run(context.Context, []string, map[string]string, executil.Options) (int, error) {
	return 0, nil
}

type bindTool struct{ tool.Base }

func (t bindTool) Binds() []tool.Bind {
	return []tool.Bind{{HostPath: "/host", SandboxPath: "/sandbox", Type: tool.BindReadOnly, Optional: true}}
}

func TestBuildCommandUsesDirectBwrapWithSelectedRunBindings(t *testing.T) {
	home := t.TempDir()
	projectRoot := filepath.Join(home, "Projects")
	projectDir := filepath.Join(projectRoot, "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	paths := config.Paths{
		Home:           home,
		ProjectRoot:    projectRoot,
		SandboxRoot:    filepath.Join(home, "Scratch", "Toby"),
		XDGRuntimeDir:  "/run/user/1234",
		PipewireCore:   "pipewire-test",
		WaylandDisplay: "wayland-test",
		XAuthority:     filepath.Join(home, ".Xauthority"),
	}
	factory := NewFactory(paths, fakeRunner{})
	sbx, err := factory.FromOptions(&tool.CommandOptions{Env: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := tool.NewRegistry(tool.RegistryParams{Tools: []tool.Tool{bindTool{Base: tool.Base{Metadata: tool.Metadata{Name: "bind"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	toolset, err := registry.Build([]string{"bind"}, "")
	if err != nil {
		t.Fatal(err)
	}
	cmd := sbx.BuildCommand([]string{"/bin/true"}, toolset)
	assertContainsSequence(t, cmd, []string{"/usr/bin/bwrap", "--die-with-parent", "--unshare-pid"})
	assertContainsSequence(t, cmd, []string{"--dev-bind", "/dev", "/dev"})
	assertContainsSequence(t, cmd, []string{"--bind-try", "/sys", "/sys"})
	assertContainsSequence(t, cmd, []string{"--tmpfs", "/run/user/1234"})
	assertContainsSequence(t, cmd, []string{"--ro-bind-try", "/run/user/1234/pulse", "/run/user/1234/pulse"})
	assertContainsSequence(t, cmd, []string{"--ro-bind-try", "/run/user/1234/pipewire-test", "/run/user/1234/pipewire-test"})
	assertContainsSequence(t, cmd, []string{"--ro-bind-try", "/run/user/1234/wayland-test", "/run/user/1234/wayland-test"})
	assertContainsSequence(t, cmd, []string{"--ro-bind-try", "/run/udev", "/run/udev"})
	assertContainsSequence(t, cmd, []string{"--ro-bind-try", paths.XAuthority, paths.XAuthority})
	assertContainsSequence(t, cmd, []string{"--bind", sbx.HomeDir(), home})
	assertContainsSequence(t, cmd, []string{"--bind", "/usr/bin/true", "/usr/bin/xdg-open"})
	assertContainsSequence(t, cmd, []string{"--ro-bind-try", "/host", "/sandbox"})
	assertContainsSequence(t, cmd, []string{"--bind-try", projectDir, projectDir})
	assertContainsSequence(t, cmd, []string{"--chdir", projectDir})
	if slices.Contains(cmd, "/run/dbus") || slices.Contains(cmd, "/run/user/1234/bus") {
		t.Fatalf("command unexpectedly includes dbus bindings: %#v", cmd)
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
