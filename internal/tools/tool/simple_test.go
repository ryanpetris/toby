package tool

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"petris.dev/toby/container/layout"
	"petris.dev/toby/container/mount"
)

func TestSimpleHostInitRegistersManagedMount(t *testing.T) {
	sandbox := &fakeSandboxService{}
	simple := &Simple{Base: Base{Metadata: Metadata{Name: "tool"}}, Sandbox: sandbox, HostSubpath: []string{"state", "tool"}}
	if err := simple.HostInit(context.Background(), &CommandOptions{}); err != nil {
		t.Fatal(err)
	}
	want := []mount.Request{{
		Key:    mount.Key{Type: mount.TypeTool, Name: "tool", Purpose: "state"},
		Target: "~/state/tool",
		Access: mount.AccessRegular,
	}}
	if !reflect.DeepEqual(sandbox.mountRequests, want) {
		t.Fatalf("mounts = %#v, want %#v", sandbox.mountRequests, want)
	}

	sandbox = &fakeSandboxService{}
	simple = &Simple{Base: Base{Metadata: Metadata{Name: "tool"}}, Sandbox: sandbox, HostSubpath: []string{"host"}, SandboxSubpath: []string{"sandbox"}, Access: mount.AccessReadOnly}
	if err := simple.HostInit(context.Background(), &CommandOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(sandbox.mountRequests) != 1 || sandbox.mountRequests[0].Target != "~/sandbox" || sandbox.mountRequests[0].Access != mount.AccessReadOnly {
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
	binds         []mount.Bind
	mountRequests []mount.Request
	mounts        map[mount.Key]mount.Mount
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
func (s *fakeSandboxService) AddBind(bind mount.Bind) error {
	s.binds = append(s.binds, bind)
	return nil
}
func (s *fakeSandboxService) AddMount(req mount.Request) (mount.Mount, error) {
	s.mountRequests = append(s.mountRequests, req)
	m := mount.Mount{Key: req.Key, Target: layout.Expand(req.Target)}
	if s.mounts == nil {
		s.mounts = map[mount.Key]mount.Mount{}
	}
	s.mounts[req.Key] = m
	return m, nil
}
func (s *fakeSandboxService) Mount(key mount.Key) (mount.Mount, bool) {
	m, ok := s.mounts[key]
	return m, ok
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
