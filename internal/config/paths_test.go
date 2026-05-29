package config

import (
	"path/filepath"
	"testing"
)

func TestNewPathsUsesXDGProjectAndCacheDirectories(t *testing.T) {
	home := t.TempDir()
	projects := filepath.Join(home, "Work")
	cacheHome := filepath.Join(home, "Cache")
	stateHome := filepath.Join(home, "State")
	t.Setenv("HOME", home)
	t.Setenv("XDG_PROJECTS_DIR", projects)
	t.Setenv("XDG_CACHE_HOME", cacheHome)
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("TOBY_SANDBOX_ROOT", "")

	paths, err := NewPaths()
	if err != nil {
		t.Fatal(err)
	}
	if paths.ProjectRoot != projects {
		t.Fatalf("ProjectRoot = %q, want %q", paths.ProjectRoot, projects)
	}
	wantSandboxRoot := filepath.Join(cacheHome, "toby", "sandboxes")
	if paths.SandboxRoot != wantSandboxRoot {
		t.Fatalf("SandboxRoot = %q, want %q", paths.SandboxRoot, wantSandboxRoot)
	}
	if paths.StateHome != stateHome {
		t.Fatalf("StateHome = %q, want %q", paths.StateHome, stateHome)
	}
}

func TestNewPathsDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_PROJECTS_DIR", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("TOBY_SANDBOX_ROOT", "")

	paths, err := NewPaths()
	if err != nil {
		t.Fatal(err)
	}
	if paths.ProjectRoot != filepath.Join(home, "Projects") {
		t.Fatalf("ProjectRoot = %q", paths.ProjectRoot)
	}
	wantSandboxRoot := filepath.Join(home, ".cache", "toby", "sandboxes")
	if paths.SandboxRoot != wantSandboxRoot {
		t.Fatalf("SandboxRoot = %q, want %q", paths.SandboxRoot, wantSandboxRoot)
	}
	wantStateHome := filepath.Join(home, ".local", "state")
	if paths.StateHome != wantStateHome {
		t.Fatalf("StateHome = %q, want %q", paths.StateHome, wantStateHome)
	}
}

func TestNewPathsUsesTobySandboxRootOverride(t *testing.T) {
	home := t.TempDir()
	sandboxRoot := filepath.Join(home, "Sandboxes")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "IgnoredCache"))
	t.Setenv("TOBY_SANDBOX_ROOT", sandboxRoot)

	paths, err := NewPaths()
	if err != nil {
		t.Fatal(err)
	}
	if paths.SandboxRoot != sandboxRoot {
		t.Fatalf("SandboxRoot = %q, want %q", paths.SandboxRoot, sandboxRoot)
	}
}
