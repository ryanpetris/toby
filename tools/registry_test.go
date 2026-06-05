package tools

import (
	"reflect"
	"testing"
)

func testTool(name string, priority int, deps ...string) Tool {
	return Base{Metadata: Metadata{Name: name, LaunchHelp: "help", Priority: priority, Dependencies: deps}}
}

func groupedTool(name, group string) Tool {
	return Base{Metadata: Metadata{Name: name, LaunchHelp: "help", Group: group, ContextGroups: []string{group}}}
}

func TestExpandGroupsAssemblesCatalogFromTools(t *testing.T) {
	registry, err := NewRegistry([]Tool{
		groupedTool("claude", GroupAI),
		groupedTool("codex", GroupAI),
		groupedTool("npm", GroupSystem),
		groupedTool("gh", GroupVCS),
	})
	if err != nil {
		t.Fatal(err)
	}
	// ai+vcs expands to the tools filed under those groups, in group order, sorted.
	if got, want := registry.ExpandGroups([]string{GroupVCS, GroupAI}), []string{"claude", "codex", "gh"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ExpandGroups = %#v, want %#v", got, want)
	}
	if got := registry.ExpandGroups([]string{GroupSystem}); !reflect.DeepEqual(got, []string{"npm"}) {
		t.Fatalf("ExpandGroups(system) = %#v", got)
	}
}

func TestNewRegistryRejectsDuplicateNames(t *testing.T) {
	_, err := NewRegistry([]Tool{testTool("a", 10), testTool("a", 20)})
	if err == nil {
		t.Fatal("expected duplicate registration to fail")
	}
}

func TestNewRegistryRejectsBadDependencyPriority(t *testing.T) {
	// dependency must have a strictly lower priority than the dependent.
	_, err := NewRegistry([]Tool{testTool("dep", 100), testTool("main", 100, "dep")})
	if err == nil {
		t.Fatal("expected dependency priority violation to fail")
	}
	_, err = NewRegistry([]Tool{testTool("missing-dep", 100, "nope")})
	if err == nil {
		t.Fatal("expected unknown dependency to fail")
	}
}

func TestBuildOrdersByPriorityThenName(t *testing.T) {
	registry, err := NewRegistry([]Tool{
		testTool("npm", 10),
		testTool("claude", 100, "npm"),
		testTool("docker", 10),
	})
	if err != nil {
		t.Fatal(err)
	}
	set, err := registry.Build([]string{"claude", "docker"}, "claude")
	if err != nil {
		t.Fatal(err)
	}
	// npm pulled in as claude's dependency; priority 10 (docker, npm) before 100 (claude).
	if got, want := set.OrderedToolNames(), []string{"docker", "npm", "claude"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %#v, want %#v", got, want)
	}
	if set.Primary() == nil || set.Primary().Name() != "claude" {
		t.Fatalf("primary = %v", set.Primary())
	}
}

func TestBuildRejectsUnknownTool(t *testing.T) {
	registry, err := NewRegistry([]Tool{testTool("npm", 10)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Build([]string{"ghost"}, ""); err == nil {
		t.Fatal("expected unknown tool to fail")
	}
}
