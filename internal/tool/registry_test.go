package tool

import (
	"context"
	"reflect"
	"testing"
)

type fakeTool struct {
	Base
	deps []string
}

func newFakeTool(name string, deps ...string) fakeTool {
	return fakeTool{Base: Base{Metadata: Metadata{Name: name}}, deps: deps}
}

func (t fakeTool) Dependencies() []string { return append([]string(nil), t.deps...) }

func TestRegistryBuildOrdersDependenciesBeforeDependents(t *testing.T) {
	registry, err := NewRegistry(RegistryParams{Tools: []Tool{
		newFakeTool("npm"),
		newFakeTool("claude", "npm"),
		newFakeTool("print"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	toolset, err := registry.Build([]string{"print", "claude"}, "claude")
	if err != nil {
		t.Fatal(err)
	}
	got := toolset.OrderedToolNames()
	want := []string{"npm", "claude", "print"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ordered names = %#v, want %#v", got, want)
	}
}

func TestRegistryBuildDetectsCycles(t *testing.T) {
	registry, err := NewRegistry(RegistryParams{Tools: []Tool{
		newFakeTool("a", "b"),
		newFakeTool("b", "a"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = registry.Build([]string{"a"}, "a")
	if err == nil {
		t.Fatal("expected cycle error")
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
