package tool

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSimpleHostInitRegistersStateBind(t *testing.T) {
	root := t.TempDir()
	stateRoot := filepath.Join(root, "state-root")
	sandbox := &fakeSandboxService{}
	simple := &Simple{Base: Base{Metadata: Metadata{Name: "tool"}}, Sandbox: sandbox, RootDir: filepath.Join(root, "private"), HostSubpath: []string{"state", "tool"}}
	if err := simple.HostInit(context.Background(), &CommandOptions{ToolStates: ToolStateSettings{Default: ToolStateConfig{State: ToolStateHost, StateRoot: stateRoot}}}); err != nil {
		t.Fatal(err)
	}
	want := []Bind{{
		HostPath: filepath.Join(stateRoot, "state", "tool"),
		Target:   homeTarget("state", "tool"),
		Type:     BindRegular,
		State:    true,
	}}
	if !reflect.DeepEqual(sandbox.binds, want) {
		t.Fatalf("binds = %#v, want %#v", sandbox.binds, want)
	}

	sandbox = &fakeSandboxService{}
	simple = &Simple{Base: Base{Metadata: Metadata{Name: "tool"}}, Sandbox: sandbox, RootDir: filepath.Join(root, "private"), HostSubpath: []string{"host"}, SandboxSubpath: []string{"sandbox"}, BindType: BindReadOnly}
	if err := simple.HostInit(context.Background(), &CommandOptions{ToolStates: ToolStateSettings{Default: ToolStateConfig{State: ToolStateHost, StateRoot: stateRoot}}}); err != nil {
		t.Fatal(err)
	}
	if len(sandbox.binds) != 1 || sandbox.binds[0].HostPath != filepath.Join(stateRoot, "sandbox") || sandbox.binds[0].Target != homeTarget("sandbox") || sandbox.binds[0].Type != BindReadOnly {
		t.Fatalf("custom binds = %#v", sandbox.binds)
	}

	sandbox = &fakeSandboxService{}
	simple = &Simple{Base: Base{Metadata: Metadata{Name: "tool"}}, Sandbox: sandbox}
	if err := simple.HostInit(context.Background(), &CommandOptions{ToolStates: ToolStateSettings{Default: ToolStateConfig{State: ToolStateHost, StateRoot: stateRoot}}}); err != nil {
		t.Fatal(err)
	}
	if len(sandbox.binds) != 0 || simple.UsesToolState() {
		t.Fatalf("empty binds = %#v, uses state = %v", sandbox.binds, simple.UsesToolState())
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
	exec  func(context.Context, []string, ExecOptions) (int, error)
	binds []Bind
}

func (s fakeSandboxService) Paths() SandboxPaths                                  { return SandboxPaths{} }
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
func (s *fakeSandboxService) AddBind(bind Bind) error {
	s.binds = append(s.binds, bind)
	return nil
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
	return s.exec(ctx, argv, opts)
}
func (s fakeSandboxService) TobyMCPURL() string { return "" }
