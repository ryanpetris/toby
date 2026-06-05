package tools

import (
	"reflect"
	"testing"
)

func testTool(name string, deps ...string) Tool {
	return Base{Metadata: Metadata{Name: name, LaunchHelp: "help", Dependencies: deps}}
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
	_, err := NewRegistry([]Tool{testTool("a"), testTool("a")})
	if err == nil {
		t.Fatal("expected duplicate registration to fail")
	}
}

func TestNewRegistryRejectsBadDependency(t *testing.T) {
	_, err := NewRegistry([]Tool{testTool("missing-dep", "nope")})
	if err == nil {
		t.Fatal("expected unknown dependency to fail")
	}
	// A dependency cycle has no valid topological order and must be rejected.
	_, err = NewRegistry([]Tool{testTool("a", "b"), testTool("b", "a")})
	if err == nil {
		t.Fatal("expected dependency cycle to fail")
	}
}

func TestBuildOrdersTopologically(t *testing.T) {
	registry, err := NewRegistry([]Tool{
		testTool("npm"),
		testTool("claude", "npm"),
		testTool("docker"),
	})
	if err != nil {
		t.Fatal(err)
	}
	set, err := registry.Build([]string{"claude", "docker"}, "claude")
	if err != nil {
		t.Fatal(err)
	}
	// npm is pulled in as claude's dependency and must precede claude; the
	// independent docker/npm sort alphabetically, then the dependent claude.
	if got, want := set.OrderedToolNames(), []string{"docker", "npm", "claude"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %#v, want %#v", got, want)
	}
	if set.Primary() == nil || set.Primary().Name() != "claude" {
		t.Fatalf("primary = %v", set.Primary())
	}
}

func TestBuildRejectsUnknownTool(t *testing.T) {
	registry, err := NewRegistry([]Tool{testTool("npm")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Build([]string{"ghost"}, ""); err == nil {
		t.Fatal("expected unknown tool to fail")
	}
}
