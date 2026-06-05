package run

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"petris.dev/toby/diagnostic/warning"
	"petris.dev/toby/tools"
)

func TestPrepareConfiguredProjectsWarnsAndSkipsMissingProjects(t *testing.T) {
	home := t.TempDir()
	existing := filepath.Join(home, "existing")
	missing := filepath.Join(home, "missing")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	opts := &tools.Options{Projects: []tools.ProjectMount{{Name: "missing", Source: missing}, {Name: "existing", Source: existing}}}
	var stderr bytes.Buffer
	if err := prepareConfiguredProjects(&stderr, home, opts, warning.Suppression{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "warning[project.missing]") || !strings.Contains(stderr.String(), missing) {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if opts.Env != "existing" || !reflect.DeepEqual(opts.Projects, []tools.ProjectMount{{Name: "existing", Source: existing}}) {
		t.Fatalf("options = %#v", opts)
	}

	stderr.Reset()
	opts = &tools.Options{Projects: []tools.ProjectMount{{Name: "missing", Source: missing}}}
	suppress := warning.Suppression{Set: true, IDs: map[warning.ID]bool{warning.ProjectMissing: true}}
	if err := prepareConfiguredProjects(&stderr, home, opts, suppress); err == nil || !strings.Contains(err.Error(), "at least one existing project") {
		t.Fatalf("error = %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("suppressed stderr = %q", stderr.String())
	}
}

func TestPrepareConfiguredProjectsWarnsAndSkipsDuplicateNames(t *testing.T) {
	home := t.TempDir()
	first := filepath.Join(home, "first")
	second := filepath.Join(home, "second")
	if err := os.MkdirAll(first, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(second, 0o755); err != nil {
		t.Fatal(err)
	}
	opts := &tools.Options{Projects: []tools.ProjectMount{{Name: "app", Source: first}, {Name: "app", Source: second}}}
	var stderr bytes.Buffer
	if err := prepareConfiguredProjects(&stderr, home, opts, warning.Suppression{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "warning[project.duplicate]") || !strings.Contains(stderr.String(), second) {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if opts.Env != "app" || !reflect.DeepEqual(opts.Projects, []tools.ProjectMount{{Name: "app", Source: first}}) {
		t.Fatalf("options = %#v", opts)
	}

	stderr.Reset()
	opts = &tools.Options{Projects: []tools.ProjectMount{{Name: "app", Source: first}, {Name: "app", Source: second}}}
	suppress := warning.Suppression{Set: true, IDs: map[warning.ID]bool{warning.ProjectDuplicate: true}}
	if err := prepareConfiguredProjects(&stderr, home, opts, suppress); err != nil {
		t.Fatal(err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("suppressed stderr = %q", stderr.String())
	}
}

func TestPrepareConfiguredProjectsAllowsSameSourceWithDifferentNames(t *testing.T) {
	home := t.TempDir()
	source := filepath.Join(home, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	opts := &tools.Options{Projects: []tools.ProjectMount{{Name: "foo", Source: source}, {Name: "bar", Source: source}}}
	var stderr bytes.Buffer
	if err := prepareConfiguredProjects(&stderr, home, opts, warning.Suppression{}); err != nil {
		t.Fatal(err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
	want := []tools.ProjectMount{{Name: "foo", Source: source}, {Name: "bar", Source: source}}
	if opts.Env != "foo" || !reflect.DeepEqual(opts.Projects, want) {
		t.Fatalf("options = %#v, want projects %#v", opts, want)
	}
}
