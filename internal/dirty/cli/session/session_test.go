package session

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"petris.dev/toby/config/toby"
	"petris.dev/toby/diagnostic/warning"
	"petris.dev/toby/tools"
)

func TestApplySandboxDefaultsUsesHostDockerDefaults(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	writeTobyConfig(t, dir, []byte(`
container:
  image: node:host
  build:
    context: docker
    dockerfile: Dockerfile.toby
`))
	cfg, err := tobyconfig.Load(dir, home)
	if err != nil {
		t.Fatal(err)
	}

	got := ApplySandboxDefaults(&tools.Options{}, cfg)
	if got.Image != "node:host" {
		t.Fatalf("defaults = %#v", got)
	}
	if got.Build.Context != filepath.Join(dir, "docker") || got.Build.Dockerfile != filepath.Join(dir, "docker", "Dockerfile.toby") {
		t.Fatalf("docker build = %#v", got.Build)
	}
}

func TestApplySandboxDefaultsPreservesExplicitLaunchValues(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	writeTobyConfig(t, dir, []byte(`
container:
  image: node:host
  build:
    context: docker
`))
	cfg, err := tobyconfig.Load(dir, home)
	if err != nil {
		t.Fatal(err)
	}

	got := ApplySandboxDefaults(&tools.Options{SandboxRuntime: "docker", Image: "node:launch"}, cfg)
	if got.Image != "node:launch" {
		t.Fatalf("defaults = %#v", got)
	}
}

func TestApplySandboxDefaultsMergesHostToolDefaults(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	writeTobyConfig(t, dir, []byte(`
tool:
  opencode:
    mountProfile: host-state
`))
	cfg, err := tobyconfig.Load(dir, home)
	if err != nil {
		t.Fatal(err)
	}

	got := ApplySandboxDefaults(&tools.Options{ToolMountProfiles: map[string]string{tools.ClaudeToolName: "private"}}, cfg)
	if got.ToolMountProfiles[tools.OpenCodeToolName] != "host-state" || got.ToolMountProfiles[tools.ClaudeToolName] != "private" {
		t.Fatalf("tool mount profiles = %#v", got.ToolMountProfiles)
	}
}

func TestApplySandboxDefaultsMergesWarningSuppression(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	writeTobyConfig(t, dir, []byte(`
settings:
  suppressWarnings: ["*"]
`))
	cfg, err := tobyconfig.Load(dir, home)
	if err != nil {
		t.Fatal(err)
	}

	got := ApplySandboxDefaults(&tools.Options{SuppressWarnings: warning.Suppression{Set: true, IDs: map[warning.ID]bool{warning.MountHostBacking: true}}}, cfg)
	if !got.SuppressWarnings.Suppresses(warning.MountHostBacking) || got.SuppressWarnings.Suppresses(warning.ModelDiscovery) {
		t.Fatalf("suppress warnings = %#v", got.SuppressWarnings)
	}
}

func TestApplySandboxDefaultsMergesDebugWithExplicitOverride(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	writeTobyConfig(t, dir, []byte(`
settings:
  debug: true
`))
	cfg, err := tobyconfig.Load(dir, home)
	if err != nil {
		t.Fatal(err)
	}

	got := ApplySandboxDefaults(&tools.Options{}, cfg)
	if !got.DebugEnabled() {
		t.Fatalf("debug = %#v", got.Debug)
	}
	debug := false
	got = ApplySandboxDefaults(&tools.Options{Debug: &debug}, cfg)
	if got.Debug == nil || got.DebugEnabled() {
		t.Fatalf("debug override = %#v", got.Debug)
	}
}

func TestApplySandboxDefaultsMergesYoloWithExplicitOverride(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	writeTobyConfig(t, dir, []byte(`
settings:
  yolo: true
`))
	cfg, err := tobyconfig.Load(dir, home)
	if err != nil {
		t.Fatal(err)
	}

	got := ApplySandboxDefaults(&tools.Options{}, cfg)
	if !got.YoloEnabled() {
		t.Fatalf("yolo = %#v", got.Yolo)
	}
	yolo := false
	got = ApplySandboxDefaults(&tools.Options{Yolo: &yolo}, cfg)
	if got.Yolo == nil || got.YoloEnabled() {
		t.Fatalf("yolo override = %#v", got.Yolo)
	}
}

func TestPrepareConfiguredProjectsWarnsAndSkipsMissingProjects(t *testing.T) {
	home := t.TempDir()
	existing := filepath.Join(home, "existing")
	missing := filepath.Join(home, "missing")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	opts := &tools.Options{Projects: []tools.ProjectMount{{Name: "missing", Source: missing}, {Name: "existing", Source: existing}}}
	var stderr bytes.Buffer
	if err := prepareConfiguredProjects(&stderr, home, opts); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "warning[project.missing]") || !strings.Contains(stderr.String(), missing) {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if opts.Env != "existing" || !reflect.DeepEqual(opts.Projects, []tools.ProjectMount{{Name: "existing", Source: existing}}) {
		t.Fatalf("options = %#v", opts)
	}

	stderr.Reset()
	opts = &tools.Options{SuppressWarnings: warning.Suppression{Set: true, IDs: map[warning.ID]bool{warning.ProjectMissing: true}}, Projects: []tools.ProjectMount{{Name: "missing", Source: missing}}}
	if err := prepareConfiguredProjects(&stderr, home, opts); err == nil || !strings.Contains(err.Error(), "at least one existing project") {
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
	if err := prepareConfiguredProjects(&stderr, home, opts); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "warning[project.duplicate]") || !strings.Contains(stderr.String(), second) {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if opts.Env != "app" || !reflect.DeepEqual(opts.Projects, []tools.ProjectMount{{Name: "app", Source: first}}) {
		t.Fatalf("options = %#v", opts)
	}

	stderr.Reset()
	opts = &tools.Options{SuppressWarnings: warning.Suppression{Set: true, IDs: map[warning.ID]bool{warning.ProjectDuplicate: true}}, Projects: []tools.ProjectMount{{Name: "app", Source: first}, {Name: "app", Source: second}}}
	if err := prepareConfiguredProjects(&stderr, home, opts); err != nil {
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
	if err := prepareConfiguredProjects(&stderr, home, opts); err != nil {
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

func writeTobyConfig(t *testing.T, dir string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}
