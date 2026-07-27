package wiring

// Docker selection isolation: the high-trust Docker capability enters a toolset
// only when the caller explicitly selects it.

import (
	"slices"
	"testing"

	"petris.dev/toby/internal/tools"
	"petris.dev/toby/internal/tools/builtin/docker"
)

func TestDockerToolIsNeverAnImplicitDependency(t *testing.T) {
	metadata := registeredMetadata()
	registered := make([]tools.Tool, 0, len(metadata))
	foundDocker := false
	for _, item := range metadata {
		registered = append(registered, tools.Base{Metadata: item})
		if item.Name == docker.Name {
			foundDocker = true
		}
	}
	if !foundDocker {
		t.Fatalf("%s tool is not registered", docker.Name)
	}

	registry, err := tools.NewRegistry(registered)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range metadata {
		if item.Name == docker.Name {
			continue
		}

		toolset, err := registry.Build([]string{item.Name}, item.Name)
		if err != nil {
			t.Fatalf("build %s toolset: %v", item.Name, err)
		}
		if slices.Contains(toolset.OrderedToolNames(), docker.Name) {
			t.Errorf("%s implicitly selects the high-trust %s tool", item.Name, docker.Name)
		}
	}
}
