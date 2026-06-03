package session

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"petris.dev/toby/internal/config/toby"
	"petris.dev/toby/internal/diagnostic/warning"
	sandboxmount "petris.dev/toby/internal/sandbox/mount"
	"petris.dev/toby/internal/tools/tool"
)

func TestApplySandboxDefaultsUsesHostDockerDefaults(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	writeTobyConfig(t, dir, []byte(`
sandbox:
  runtime:
    default: docker
    docker:
      image: node:host
      home: /home/host
      projects: /workspace/host
      build:
        context: docker
        dockerfile: Dockerfile.toby
    bubblewrap:
      root: ./sandboxes
mountProfiles:
  default:
    backing: host
    hostRoot: ./state
`))
	cfg, err := tobyconfig.Load(dir, home)
	if err != nil {
		t.Fatal(err)
	}

	got := ApplySandboxDefaults(&tool.CommandOptions{}, cfg)
	if got.SandboxRuntime != "docker" || got.DockerImage != "node:host" || got.DockerHome != "/home/host" || got.DockerProjects != "/workspace/host" {
		t.Fatalf("defaults = %#v", got)
	}
	if got.DockerBuild.Context != filepath.Join(dir, "docker") || got.DockerBuild.Dockerfile != filepath.Join(dir, "docker", "Dockerfile.toby") {
		t.Fatalf("docker build = %#v", got.DockerBuild)
	}
	if got.BubblewrapRoot != filepath.Join(dir, "sandboxes") {
		t.Fatalf("defaults = %#v", got)
	}
	mounts := got.MountProfiles.Config("default")
	if mounts.Backing != sandboxmount.BackingHost || mounts.HostRoot != filepath.Join(dir, "state") {
		t.Fatalf("mounts = %#v", mounts)
	}
}

func TestApplySandboxDefaultsPreservesExplicitLaunchValues(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	writeTobyConfig(t, dir, []byte(`
sandbox:
  runtime:
    default: docker
    docker:
      image: node:host
      home: /home/host
      projects: /workspace/host
      build:
        context: docker
`))
	cfg, err := tobyconfig.Load(dir, home)
	if err != nil {
		t.Fatal(err)
	}

	got := ApplySandboxDefaults(&tool.CommandOptions{SandboxRuntime: "docker", DockerImage: "node:launch", DockerHome: "/home/launch", DockerProjects: "/workspace/launch"}, cfg)
	if got.DockerImage != "node:launch" || got.DockerHome != "/home/launch" || got.DockerProjects != "/workspace/launch" {
		t.Fatalf("defaults = %#v", got)
	}
}

func TestApplySandboxDefaultsMergesLaunchMountOverrides(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	writeTobyConfig(t, dir, []byte(`
mountProfiles:
  default:
    backing: host
    hostRoot: ~/state/default
`))
	cfg, err := tobyconfig.Load(dir, home)
	if err != nil {
		t.Fatal(err)
	}

	opencodeKey := sandboxmount.Key{Type: sandboxmount.TypeTool, Name: tool.OpenCodeToolName, Purpose: "config"}
	claudeKey := sandboxmount.Key{Type: sandboxmount.TypeTool, Name: tool.ClaudeToolName, Purpose: "state"}
	got := ApplySandboxDefaults(&tool.CommandOptions{MountProfiles: sandboxmount.Profiles{"default": {Backing: sandboxmount.BackingPrivate}}}, cfg)
	mounts := got.MountProfiles.Config("default")
	if mounts.BackingFor(opencodeKey) != sandboxmount.BackingPrivate || mounts.BackingFor(claudeKey) != sandboxmount.BackingPrivate {
		t.Fatalf("mounts = %#v", mounts)
	}
	if mounts.HostRootFor(opencodeKey) != filepath.Join(home, "state", "default") {
		t.Fatalf("mount roots = %#v", mounts)
	}
}

func TestApplySandboxDefaultsMergesHostToolDefaults(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	writeTobyConfig(t, dir, []byte(`
tools:
  opencode:
    mountProfile: host-state
`))
	cfg, err := tobyconfig.Load(dir, home)
	if err != nil {
		t.Fatal(err)
	}

	got := ApplySandboxDefaults(&tool.CommandOptions{ToolMountProfiles: map[string]string{tool.ClaudeToolName: "private"}}, cfg)
	if got.ToolMountProfiles[tool.OpenCodeToolName] != "host-state" || got.ToolMountProfiles[tool.ClaudeToolName] != "private" {
		t.Fatalf("tool mount profiles = %#v", got.ToolMountProfiles)
	}
}

func TestApplySandboxDefaultsMergesWarningSuppression(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	writeTobyConfig(t, dir, []byte(`
settings:
  suppressWarnings: true
`))
	cfg, err := tobyconfig.Load(dir, home)
	if err != nil {
		t.Fatal(err)
	}

	got := ApplySandboxDefaults(&tool.CommandOptions{SuppressWarnings: warning.Suppression{Set: true, IDs: map[warning.ID]bool{warning.MountHostBacking: true}}}, cfg)
	if !got.SuppressWarnings.Suppresses(warning.MountHostBacking) || got.SuppressWarnings.Suppresses(warning.OpenCodeModelDiscovery) {
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

	got := ApplySandboxDefaults(&tool.CommandOptions{}, cfg)
	if !got.DebugEnabled() {
		t.Fatalf("debug = %#v", got.Debug)
	}
	debug := false
	got = ApplySandboxDefaults(&tool.CommandOptions{Debug: &debug}, cfg)
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

	got := ApplySandboxDefaults(&tool.CommandOptions{}, cfg)
	if !got.YoloEnabled() {
		t.Fatalf("yolo = %#v", got.Yolo)
	}
	yolo := false
	got = ApplySandboxDefaults(&tool.CommandOptions{Yolo: &yolo}, cfg)
	if got.Yolo == nil || got.YoloEnabled() {
		t.Fatalf("yolo override = %#v", got.Yolo)
	}
}

func TestWarnHostBackedMounts(t *testing.T) {
	mounts := []sandboxmount.Info{{Key: sandboxmount.Key{Type: sandboxmount.TypeTool, Name: tool.OpenCodeToolName, Purpose: "config"}}}
	var stderr bytes.Buffer
	warnHostBackedMounts(&stderr, warning.Suppression{}, mounts)
	if got := stderr.String(); got == "" || !bytes.Contains(stderr.Bytes(), []byte("warning[mount.host-backing]")) || !bytes.Contains(stderr.Bytes(), []byte("tool.opencode.config")) {
		t.Fatalf("warning = %q", got)
	}
	stderr.Reset()
	warnHostBackedMounts(&stderr, warning.Suppression{Set: true, IDs: map[warning.ID]bool{warning.MountHostBacking: true}}, mounts)
	if stderr.Len() != 0 {
		t.Fatalf("suppressed warning = %q", stderr.String())
	}

	stderr.Reset()
	warnHostBackedMounts(&stderr, warning.Suppression{}, nil)
	if stderr.Len() != 0 {
		t.Fatalf("empty warning = %q", stderr.String())
	}
}

func TestApplySandboxDefaultsDoesNotApplyDormantDockerDefaults(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	writeTobyConfig(t, dir, []byte(`
sandbox:
  runtime:
    docker:
      image: node:host
      home: /home/host
      projects: /workspace/host
`))
	cfg, err := tobyconfig.Load(dir, home)
	if err != nil {
		t.Fatal(err)
	}

	got := ApplySandboxDefaults(&tool.CommandOptions{}, cfg)
	if got.DockerImage != "" || got.DockerHome != "" || got.DockerProjects != "" || got.DockerBuild.IsSet() {
		t.Fatalf("defaults = %#v", got)
	}
}

func TestPrepareConfiguredProjectsWarnsAndSkipsMissingProjects(t *testing.T) {
	home := t.TempDir()
	existing := filepath.Join(home, "existing")
	missing := filepath.Join(home, "missing")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	opts := &tool.CommandOptions{Projects: []tool.ProjectMount{{Name: "missing", Source: missing}, {Name: "existing", Source: existing}}}
	var stderr bytes.Buffer
	if err := prepareConfiguredProjects(&stderr, home, opts); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "warning[project.missing]") || !strings.Contains(stderr.String(), missing) {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if opts.Env != "existing" || !reflect.DeepEqual(opts.Projects, []tool.ProjectMount{{Name: "existing", Source: existing}}) {
		t.Fatalf("options = %#v", opts)
	}

	stderr.Reset()
	opts = &tool.CommandOptions{SuppressWarnings: warning.Suppression{Set: true, IDs: map[warning.ID]bool{warning.ProjectMissing: true}}, Projects: []tool.ProjectMount{{Name: "missing", Source: missing}}}
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
	opts := &tool.CommandOptions{Projects: []tool.ProjectMount{{Name: "app", Source: first}, {Name: "app", Source: second}}}
	var stderr bytes.Buffer
	if err := prepareConfiguredProjects(&stderr, home, opts); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "warning[project.duplicate]") || !strings.Contains(stderr.String(), second) {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if opts.Env != "app" || !reflect.DeepEqual(opts.Projects, []tool.ProjectMount{{Name: "app", Source: first}}) {
		t.Fatalf("options = %#v", opts)
	}

	stderr.Reset()
	opts = &tool.CommandOptions{SuppressWarnings: warning.Suppression{Set: true, IDs: map[warning.ID]bool{warning.ProjectDuplicate: true}}, Projects: []tool.ProjectMount{{Name: "app", Source: first}, {Name: "app", Source: second}}}
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
	opts := &tool.CommandOptions{Projects: []tool.ProjectMount{{Name: "foo", Source: source}, {Name: "bar", Source: source}}}
	var stderr bytes.Buffer
	if err := prepareConfiguredProjects(&stderr, home, opts); err != nil {
		t.Fatal(err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
	want := []tool.ProjectMount{{Name: "foo", Source: source}, {Name: "bar", Source: source}}
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
