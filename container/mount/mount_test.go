package mount

import (
	"context"
	"reflect"
	"testing"

	"petris.dev/toby/container/layout"
)

func TestConfigureRegistersRuntimeHome(t *testing.T) {
	s := New()
	if err := s.Configure(Config{Profile: "default", SandboxName: "demo"}); err != nil {
		t.Fatal(err)
	}

	home, ok := s.Mount(RuntimeHomeKey("demo"))
	if !ok {
		t.Fatal("runtime home not registered")
	}
	if home.Target != layout.Home || home.Volume != "toby.default.runtime.home.demo" {
		t.Fatalf("home mount = %#v", home)
	}
}

func TestAddMountExpandsTargetAndResolvesProfile(t *testing.T) {
	s := New()
	if err := s.Configure(Config{Profile: "default", SandboxName: "demo", ToolProfiles: map[string]string{"claude": "work"}}); err != nil {
		t.Fatal(err)
	}

	opencode, err := s.AddMount(Request{Key: Key{Type: TypeTool, Name: "opencode", Purpose: "config"}, Target: "~/.config/opencode"})
	if err != nil {
		t.Fatal(err)
	}
	if opencode.Target != layout.Home+"/.config/opencode" {
		t.Fatalf("target = %q", opencode.Target)
	}
	if opencode.Volume != "toby.default.tool.opencode.config" {
		t.Fatalf("volume = %q", opencode.Volume)
	}
	if opencode.SetupPath == "" || opencode.SetupPath == opencode.Target {
		t.Fatalf("setup path = %q", opencode.SetupPath)
	}

	// Per-tool profile override drives the volume namespace.
	claude, err := s.AddMount(Request{Key: Key{Type: TypeTool, Name: "claude", Purpose: "state"}, Target: "~/.config/claude"})
	if err != nil {
		t.Fatal(err)
	}
	if claude.Profile != "work" || claude.Volume != "toby.work.tool.claude.state" {
		t.Fatalf("claude mount = %#v", claude)
	}
}

func TestAddMountConflictAndDedup(t *testing.T) {
	s := New()
	if err := s.Configure(Config{SandboxName: "demo"}); err != nil {
		t.Fatal(err)
	}

	req := Request{Key: Key{Type: TypeTool, Name: "opencode", Purpose: "config"}, Target: "~/.config/opencode"}
	first, err := s.AddMount(req)
	if err != nil {
		t.Fatal(err)
	}

	again, err := s.AddMount(req)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, again) {
		t.Fatalf("dedup mismatch: %#v vs %#v", first, again)
	}

	if _, err := s.AddMount(Request{Key: req.Key, Target: "~/other"}); err == nil {
		t.Fatal("expected conflicting target to fail")
	}
}

func TestAddBindExpandsAndDedups(t *testing.T) {
	s := New()
	if err := s.Configure(Config{SandboxName: "demo"}); err != nil {
		t.Fatal(err)
	}

	bind := Bind{HostPath: "/var/run/docker.sock", Target: "/var/run/docker.sock", Access: AccessDev}
	if err := s.AddBind(bind); err != nil {
		t.Fatal(err)
	}
	if err := s.AddBind(bind); err != nil {
		t.Fatal(err)
	}
	if err := s.AddBind(Bind{HostPath: "/host/.docker", Target: "~/.docker", Access: AccessReadOnly}); err != nil {
		t.Fatal(err)
	}

	binds := s.Binds()
	if len(binds) != 2 {
		t.Fatalf("binds = %#v", binds)
	}
	if binds[1].Target != layout.Home+"/.docker" {
		t.Fatalf("bind target not expanded: %#v", binds[1])
	}
}

type fakeRunner struct{ calls [][]string }

func (r *fakeRunner) Exec(_ context.Context, argv []string, root bool) (int, error) {
	if !root {
		return 1, nil
	}

	r.calls = append(r.calls, argv)
	return 0, nil
}

func TestRunSetupChownsVolumesByDefault(t *testing.T) {
	s := New()
	if err := s.Configure(Config{SandboxName: "demo"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMount(Request{Key: Key{Type: TypeTool, Name: "opencode", Purpose: "config"}, Target: "~/.config/opencode"}); err != nil {
		t.Fatal(err)
	}

	runner := &fakeRunner{}
	if err := s.RunSetup(context.Background(), runner); err != nil {
		t.Fatal(err)
	}

	// One batch chown covering every volume's setup path (runtime home + opencode).
	if len(runner.calls) != 1 {
		t.Fatalf("calls = %#v", runner.calls)
	}
	argv := runner.calls[0]
	if len(argv) != 5 || argv[0] != "chown" || argv[1] != "-R" {
		t.Fatalf("chown argv = %#v", argv)
	}
}

func TestRunSetupInvokesPerMountHook(t *testing.T) {
	s := New()
	if err := s.Configure(Config{SandboxName: "demo"}); err != nil {
		t.Fatal(err)
	}

	var gotPath string
	_, err := s.AddMount(Request{
		Key:    Key{Type: TypeTool, Name: "opencode", Purpose: "config"},
		Target: "~/.config/opencode",
		Setup: func(_ context.Context, setupPath string, _ Executor) error {
			gotPath = setupPath
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.RunSetup(context.Background(), &fakeRunner{}); err != nil {
		t.Fatal(err)
	}
	if gotPath == "" {
		t.Fatal("custom setup hook was not invoked")
	}
}

func TestVolumeNaming(t *testing.T) {
	got := Volume("default", Key{Type: TypeTool, Name: "claude", Purpose: "state"})
	if got != "toby.default.tool.claude.state" {
		t.Fatalf("volume = %q", got)
	}
}

func TestAddMountRequiresConfigure(t *testing.T) {
	s := New()
	if _, err := s.AddMount(Request{Key: Key{Type: TypeTool, Name: "x", Purpose: "y"}, Target: "~/x"}); err == nil {
		t.Fatal("expected unconfigured service to reject AddMount")
	}
}
