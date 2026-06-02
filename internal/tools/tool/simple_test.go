package tool

import (
	"context"
	"errors"
	"reflect"
	"testing"

	sandboxmount "petris.dev/toby/internal/sandbox/mount"
	sandboxpath "petris.dev/toby/internal/sandbox/path"
)

func TestSimpleHostInitRegistersManagedMount(t *testing.T) {
	sandbox := &fakeSandboxService{}
	simple := &Simple{Base: Base{Metadata: Metadata{Name: "tool"}}, Sandbox: sandbox, HostSubpath: []string{"state", "tool"}}
	if err := simple.HostInit(context.Background(), &CommandOptions{}); err != nil {
		t.Fatal(err)
	}
	want := []sandboxmount.Request{{
		Key:     sandboxmount.Key{Type: sandboxmount.TypeTool, Name: "tool", Purpose: "state"},
		Target:  sandboxpath.HomePath("state", "tool"),
		Subpath: "state/tool",
		Access:  sandboxmount.AccessRegular,
	}}
	if !reflect.DeepEqual(sandbox.mountRequests, want) {
		t.Fatalf("mounts = %#v, want %#v", sandbox.mountRequests, want)
	}

	sandbox = &fakeSandboxService{}
	simple = &Simple{Base: Base{Metadata: Metadata{Name: "tool"}}, Sandbox: sandbox, HostSubpath: []string{"host"}, SandboxSubpath: []string{"sandbox"}, Access: sandboxmount.AccessReadOnly}
	if err := simple.HostInit(context.Background(), &CommandOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(sandbox.mountRequests) != 1 || sandbox.mountRequests[0].Target != sandboxpath.HomePath("sandbox") || sandbox.mountRequests[0].Access != sandboxmount.AccessReadOnly {
		t.Fatalf("custom mounts = %#v", sandbox.mountRequests)
	}

	sandbox = &fakeSandboxService{}
	simple = &Simple{Base: Base{Metadata: Metadata{Name: "tool"}}, Sandbox: sandbox}
	if err := simple.HostInit(context.Background(), &CommandOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(sandbox.mountRequests) != 0 || simple.UsesManagedMounts() {
		t.Fatalf("empty mounts = %#v, uses managed mounts = %v", sandbox.mountRequests, simple.UsesManagedMounts())
	}
}

func TestSimpleLaunchUsesDefaultAndOverrideCommands(t *testing.T) {
	var calls [][]string
	sandbox := &fakeSandboxService{exec: func(_ context.Context, argv []string, _ ExecOptions) (int, error) {
		calls = append(calls, append([]string(nil), argv...))
		return 0, nil
	}}
	extra := []string{"--help"}
	if err := (&Simple{Base: Base{Metadata: Metadata{Name: "tool"}}, Sandbox: sandbox}).Launch(context.Background(), extra); err != nil {
		t.Fatal(err)
	}
	if err := (&Simple{Base: Base{Metadata: Metadata{Name: "tool"}}, Sandbox: sandbox, LaunchCommand: "custom"}).Launch(context.Background(), extra); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"tool", "--help"}, {"custom", "--help"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestSimpleLaunchReturnsExecError(t *testing.T) {
	sentinel := errors.New("boom")
	simple := &Simple{Base: Base{Metadata: Metadata{Name: "tool"}}, Sandbox: &fakeSandboxService{exec: func(context.Context, []string, ExecOptions) (int, error) { return 0, sentinel }}}
	if err := simple.Launch(context.Background(), nil); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want sentinel", err)
	}
}

type fakeSandboxService struct {
	exec          func(context.Context, []string, ExecOptions) (int, error)
	binds         []sandboxmount.Bind
	mountRequests []sandboxmount.Request
	mounts        map[sandboxmount.Key]sandboxmount.Info
}

func (s fakeSandboxService) Paths() sandboxpath.Paths {
	return sandboxpath.Defaults()
}
func (s fakeSandboxService) ProjectPath(string) (string, bool)                    { return "", false }
func (s fakeSandboxService) VisibleHostPath(string) (string, error)               { return "", nil }
func (s fakeSandboxService) GetEnvironment(string) (string, bool)                 { return "", false }
func (s fakeSandboxService) SetEnvironment(context.Context, string, string) error { return nil }
func (s fakeSandboxService) PrependEnvironment(context.Context, string, string, string) error {
	return nil
}
func (s fakeSandboxService) AppendEnvironment(context.Context, string, string, string) error {
	return nil
}
func (s *fakeSandboxService) AddBind(bind sandboxmount.Bind) error {
	s.binds = append(s.binds, bind)
	return nil
}
func (s *fakeSandboxService) AddMount(req sandboxmount.Request) (sandboxmount.Info, error) {
	s.mountRequests = append(s.mountRequests, req)
	info := sandboxmount.Info{Key: req.Key, Target: sandboxpath.Resolve(req.Target, s.Paths()), Active: true}
	if s.mounts == nil {
		s.mounts = map[sandboxmount.Key]sandboxmount.Info{}
	}
	s.mounts[req.Key] = info
	return info, nil
}
func (s *fakeSandboxService) Mount(key sandboxmount.Key) (sandboxmount.Info, bool) {
	info, ok := s.mounts[key]
	return info, ok
}
func (s fakeSandboxService) AddFile(context.Context, string, []byte, uint32) error { return nil }
func (s fakeSandboxService) AddFileOwned(context.Context, string, []byte, uint32, int, int) error {
	return nil
}
func (s fakeSandboxService) DeletePath(context.Context, string, bool) error { return nil }
func (s fakeSandboxService) Mkdir(context.Context, string, uint32) error    { return nil }
func (s fakeSandboxService) MkdirOwned(context.Context, string, uint32, int, int) error {
	return nil
}
func (s fakeSandboxService) Symlink(context.Context, string, string) error { return nil }
func (s fakeSandboxService) SymlinkOwned(context.Context, string, string, int, int) error {
	return nil
}
func (s fakeSandboxService) Exec(ctx context.Context, argv []string, opts ExecOptions) (int, error) {
	if s.exec == nil {
		return 0, nil
	}
	return s.exec(ctx, argv, opts)
}
func (s fakeSandboxService) TobyMCPURL() string { return "" }
