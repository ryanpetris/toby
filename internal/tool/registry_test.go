package tool

import (
	"context"
	"reflect"
	"testing"
)

type fakeTool struct {
	Base
}

func newFakeTool(name string) fakeTool {
	return fakeTool{Base: Base{Metadata: Metadata{Name: name}}}
}

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
