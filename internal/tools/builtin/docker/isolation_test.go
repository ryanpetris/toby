package docker

// Production dependency isolation: the selected Docker CLI capability stays
// independent from Docker SDKs.

import (
	"os/exec"
	"strings"
	"testing"
)

func TestProductionDependenciesExcludeDockerRuntime(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "go", "list", "-deps", "-f", "{{.ImportPath}}", ".")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("list production dependencies: %v\n%s", err, output)
	}

	forbidden := []string{
		"github.com/docker/",
		"github.com/moby/",
		"github.com/testcontainers/",
	}
	for dependency := range strings.Lines(string(output)) {
		dependency = strings.TrimSpace(dependency)
		for _, prefix := range forbidden {
			if strings.HasPrefix(dependency, prefix) {
				t.Errorf("production dependency %q couples the Docker tool to %q", dependency, prefix)
			}
		}
	}
}
