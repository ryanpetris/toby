package config

// Tests XDG path resolution and validation.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewPathsUsesXDGProjectAndCacheDirectories(t *testing.T) {
	home := t.TempDir()
	projects := filepath.Join(home, "Work")
	cacheHome := filepath.Join(home, "Cache")
	configHome := filepath.Join(home, "Config")
	dataHome := filepath.Join(home, "Data")
	runtimeDir := filepath.Join(home, "Runtime")
	if err := os.Mkdir(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_PROJECTS_DIR", projects)
	t.Setenv("XDG_CACHE_HOME", cacheHome)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	paths, err := NewPaths()
	if err != nil {
		t.Fatal(err)
	}
	if paths.ProjectRoot != projects {
		t.Fatalf("ProjectRoot = %q, want %q", paths.ProjectRoot, projects)
	}
	if paths.XDGConfigHome != configHome {
		t.Fatalf("XDGConfigHome = %q, want %q", paths.XDGConfigHome, configHome)
	}
	if paths.XDGCacheHome != cacheHome || paths.XDGDataHome != dataHome || paths.XDGRuntimeDir != runtimeDir {
		t.Fatalf("XDG paths = %#v", paths)
	}
	runtimePaths, err := paths.ResolveRuntime()
	if err != nil {
		t.Fatal(err)
	}
	if runtimePaths.Root != filepath.Join(runtimeDir, "toby") ||
		runtimePaths.Runs != filepath.Join(runtimeDir, "toby", "runs") ||
		runtimePaths.Caddy != filepath.Join(runtimeDir, "toby", "caddy") ||
		runtimePaths.AgentSocket != filepath.Join(runtimeDir, "toby", "agent.sock") {
		t.Fatalf("runtime paths = %#v", runtimePaths)
	}
}

func TestNewPathsDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_PROJECTS_DIR", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_RUNTIME_DIR", "")

	paths, err := NewPaths()
	if err != nil {
		t.Fatal(err)
	}
	if paths.ProjectRoot != filepath.Join(home, "Projects") {
		t.Fatalf("ProjectRoot = %q", paths.ProjectRoot)
	}
	if paths.TobyConfigDir() != filepath.Join(home, ".config", "toby") {
		t.Fatalf("TobyConfigDir = %q", paths.TobyConfigDir())
	}
	if paths.RunStorageDir() != filepath.Join(home, ".cache", "toby", "runs") {
		t.Fatalf("RunStorageDir = %q", paths.RunStorageDir())
	}
	if paths.TobyDataDir() != filepath.Join(home, ".local", "share", "toby") {
		t.Fatalf("TobyDataDir = %q", paths.TobyDataDir())
	}
	if _, err := paths.ResolveRuntime(); err == nil || !strings.Contains(err.Error(), "XDG_RUNTIME_DIR is required") {
		t.Fatalf("ResolveRuntime error = %v", err)
	}
}

func TestExpandHomeOnlyExpandsLeadingTilde(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "home", "demo")
	if got, want := ExpandHome("~/work/../project", home), home+"/work/../project"; got != want {
		t.Fatalf("ExpandHome = %q, want %q", got, want)
	}
	if got, want := ExpandHome("~", home), home; got != want {
		t.Fatalf("ExpandHome = %q, want %q", got, want)
	}
	if got, want := ExpandHome("/tmp/~", home), "/tmp/~"; got != want {
		t.Fatalf("ExpandHome = %q, want %q", got, want)
	}
}

func TestNewPathsRejectsRelativeXDGPaths(t *testing.T) {
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_RUNTIME_DIR", "XDG_PROJECTS_DIR"} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", "")
			t.Setenv("XDG_CACHE_HOME", "")
			t.Setenv("XDG_DATA_HOME", "")
			t.Setenv("XDG_RUNTIME_DIR", "")
			t.Setenv("XDG_PROJECTS_DIR", "")
			t.Setenv(name, "relative/path")

			if _, err := NewPaths(); err == nil || !strings.Contains(err.Error(), name+" must be an absolute path") {
				t.Fatalf("NewPaths error = %v", err)
			}
		})
	}
}

func TestResolveRuntimeDefersFilesystemAccessPolicy(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "runtime")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	resolved, err := (Paths{XDGRuntimeDir: link}).ResolveRuntime()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := resolved.Root, filepath.Join(link, "toby"); got != want {
		t.Fatalf("runtime root = %q, want %q", got, want)
	}
}
