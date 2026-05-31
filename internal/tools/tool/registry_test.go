package tool

import (
	"context"
	"reflect"
	"testing"
)

type fakeTool struct {
	Base
	binds []Bind
}

func newFakeTool(name string) fakeTool {
	return fakeTool{Base: Base{Metadata: Metadata{Name: name}}}
}

func (t fakeTool) Binds() []Bind { return append([]Bind(nil), t.binds...) }

type lifecycleTool struct {
	Base
	dep   Tool
	calls *[]string
}

func newLifecycleTool(name string, calls *[]string, dep Tool) *lifecycleTool {
	return &lifecycleTool{Base: Base{Metadata: Metadata{Name: name}}, calls: calls, dep: dep}
}

func (t *lifecycleTool) Install(ctx context.Context, run *RunContext) error {
	if t.dep != nil {
		if err := t.dep.Install(ctx, run); err != nil {
			return err
		}
	}
	return InstallOnce(run, t.Name(), func() error {
		*t.calls = append(*t.calls, "install:"+t.Name())
		return nil
	})
}

func (t *lifecycleTool) Upgrade(ctx context.Context, run *RunContext) error {
	if t.dep != nil {
		if err := t.dep.Upgrade(ctx, run); err != nil {
			return err
		}
	}
	return UpgradeOnce(run, t.Name(), func() error {
		*t.calls = append(*t.calls, "upgrade:"+t.Name())
		return nil
	})
}

type contextLifecycleTool struct {
	Base
	dep   ContextFileTool
	calls *[]string
}

func newContextLifecycleTool(name string, calls *[]string, dep ContextFileTool) *contextLifecycleTool {
	return &contextLifecycleTool{Base: Base{Metadata: Metadata{Name: name}}, calls: calls, dep: dep}
}

func (t *contextLifecycleTool) RegisterContextFiles(ctx context.Context, run *RunContext) error {
	if t.dep != nil {
		if err := t.dep.RegisterContextFiles(ctx, run); err != nil {
			return err
		}
	}
	return RegisterContextFilesOnce(run, t.Name(), func() error {
		*t.calls = append(*t.calls, "context:"+t.Name())
		return nil
	})
}

func TestRegistryBuildPreservesExplicitSelectionOrder(t *testing.T) {
	registry, err := NewRegistry(RegistryParams{Tools: []Tool{
		newFakeTool("npm"),
		newFakeTool("claude"),
		newFakeTool("docker"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	toolset, err := registry.Build([]string{"docker", "claude", "docker"}, "claude")
	if err != nil {
		t.Fatal(err)
	}
	got := toolset.OrderedToolNames()
	want := []string{"docker", "claude"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ordered names = %#v, want %#v", got, want)
	}
}

func TestRegistryBuildAppendsMissingPrimary(t *testing.T) {
	registry, err := NewRegistry(RegistryParams{Tools: []Tool{
		newFakeTool("a"),
		newFakeTool("b"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	toolset, err := registry.Build([]string{"a"}, "b")
	if err != nil {
		t.Fatal(err)
	}
	got := toolset.OrderedToolNames()
	want := []string{"a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ordered names = %#v, want %#v", got, want)
	}
}

func TestExpandGroupsUsesConfiguredOrderAndSortsWithinGroups(t *testing.T) {
	got := ExpandGroups([]string{GroupVCS, GroupSystem})
	want := []string{DockerToolName, NpmToolName, UvToolName, ForgejoCliToolName, GitHubCliToolName, GitLabCliToolName}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExpandGroups = %#v, want %#v", got, want)
	}
}

func TestToolsetLifecycleStopsOnError(t *testing.T) {
	ctx := context.Background()
	registry, err := NewRegistry(RegistryParams{Tools: []Tool{newFakeTool("a")}})
	if err != nil {
		t.Fatal(err)
	}
	toolset, err := registry.Build([]string{"a"}, "a")
	if err != nil {
		t.Fatal(err)
	}
	if err := toolset.HostInit(ctx, &CommandOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestToolsetBindsFiltersStateBinds(t *testing.T) {
	home := t.TempDir()
	stateBind := Bind{HostPath: "/host/opencode", Target: HomeTarget(".config", "opencode"), State: true}
	dockerState := Bind{HostPath: "/host/docker", Target: HomeTarget(".docker"), State: true}
	dockerSocket := Bind{HostPath: "/var/run/docker.sock", Target: AbsoluteTarget("/var/run/docker.sock"), Type: BindDev}
	resolvedStateBind := stateBind
	resolvedStateBind.HostPath = home + "/.config/opencode"
	resolvedDockerState := dockerState
	resolvedDockerState.HostPath = home + "/.docker"
	registry, err := NewRegistry(RegistryParams{Tools: []Tool{
		fakeTool{Base: Base{Metadata: Metadata{Name: OpenCodeToolName}}, binds: []Bind{stateBind}},
		fakeTool{Base: Base{Metadata: Metadata{Name: DockerToolName}}, binds: []Bind{dockerState, dockerSocket}},
	}})
	if err != nil {
		t.Fatal(err)
	}

	toolset, err := registry.Build([]string{OpenCodeToolName, DockerToolName}, "")
	if err != nil {
		t.Fatal(err)
	}
	toolset.SetToolStates(ToolStateSettings{Default: ToolStateConfig{State: ToolStatePrivate, StateRoot: home}, Tools: map[string]ToolStateConfig{DockerToolName: {State: ToolStateHost}}})
	got := toolset.Binds()
	want := []Bind{resolvedDockerState, dockerSocket}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("private/default binds = %#v, want %#v", got, want)
	}

	toolset.SetToolStates(ToolStateSettings{Default: ToolStateConfig{State: ToolStateHost, StateRoot: home}, Tools: map[string]ToolStateConfig{DockerToolName: {State: ToolStatePrivate}}})
	got = toolset.Binds()
	want = []Bind{resolvedStateBind, dockerSocket}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("host/private docker binds = %#v, want %#v", got, want)
	}
}

func TestToolsetInstallDeduplicatesSharedServiceDependency(t *testing.T) {
	ctx := context.Background()
	var calls []string
	dep := newLifecycleTool("dep", &calls, nil)
	a := newLifecycleTool("a", &calls, dep)
	b := newLifecycleTool("b", &calls, dep)
	registry, err := NewRegistry(RegistryParams{Tools: []Tool{a, b}})
	if err != nil {
		t.Fatal(err)
	}
	toolset, err := registry.Build([]string{"a", "b"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := toolset.Install(ctx, &RunContext{Options: &CommandOptions{}}); err != nil {
		t.Fatal(err)
	}
	want := []string{"install:dep", "install:a", "install:b"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestToolsetUpgradeUsesDependencyUpgradeLifecycle(t *testing.T) {
	ctx := context.Background()
	var calls []string
	dep := newLifecycleTool("dep", &calls, nil)
	a := newLifecycleTool("a", &calls, dep)
	registry, err := NewRegistry(RegistryParams{Tools: []Tool{a}})
	if err != nil {
		t.Fatal(err)
	}
	toolset, err := registry.Build([]string{"a"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := toolset.Upgrade(ctx, &RunContext{Options: &CommandOptions{}}); err != nil {
		t.Fatal(err)
	}
	want := []string{"upgrade:dep", "upgrade:a"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestToolsetRegisterContextFilesDeduplicatesSharedDependency(t *testing.T) {
	ctx := context.Background()
	var calls []string
	dep := newContextLifecycleTool("dep", &calls, nil)
	a := newContextLifecycleTool("a", &calls, dep)
	b := newContextLifecycleTool("b", &calls, dep)
	registry, err := NewRegistry(RegistryParams{Tools: []Tool{a, b}})
	if err != nil {
		t.Fatal(err)
	}
	toolset, err := registry.Build([]string{"a", "b"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := toolset.RegisterContextFiles(ctx, &RunContext{Options: &CommandOptions{}}); err != nil {
		t.Fatal(err)
	}
	want := []string{"context:dep", "context:a", "context:b"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}
