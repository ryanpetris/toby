package config

import (
	"path/filepath"
	"testing"
)

func TestNewPathsUsesXDGProjectAndCacheDirectories(t *testing.T) {
	home := t.TempDir()
	projects := filepath.Join(home, "Work")
	cacheHome := filepath.Join(home, "Cache")
	configHome := filepath.Join(home, "Config")
	runtimeDir := filepath.Join(home, "Runtime")
	t.Setenv("HOME", home)
	t.Setenv("XDG_PROJECTS_DIR", projects)
	t.Setenv("XDG_CACHE_HOME", cacheHome)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_CONFIG_DIR", "")
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
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
	if paths.XDGRuntimeDir != runtimeDir {
		t.Fatalf("XDGRuntimeDir = %q, want %q", paths.XDGRuntimeDir, runtimeDir)
	}
	if paths.XDGConfigHome != configHome {
		t.Fatalf("XDGConfigHome = %q, want %q", paths.XDGConfigHome, configHome)
	}
}

func TestNewPathsDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_PROJECTS_DIR", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_CONFIG_DIR", "")
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(home, "Runtime"))
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
	if paths.TobyConfigDir() != filepath.Join(home, ".config", "toby") {
		t.Fatalf("TobyConfigDir = %q", paths.TobyConfigDir())
	}
}

func TestNewPathsUsesLegacyXDGConfigDirFallback(t *testing.T) {
	home := t.TempDir()
	configDir := filepath.Join(home, "ConfigDir")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_CONFIG_DIR", configDir)
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(home, "Runtime"))
	t.Setenv("TOBY_SANDBOX_ROOT", "")

	paths, err := NewPaths()
	if err != nil {
		t.Fatal(err)
	}
	if paths.TobyConfigDir() != filepath.Join(configDir, "toby") {
		t.Fatalf("TobyConfigDir = %q", paths.TobyConfigDir())
	}
}

func TestNewPathsRequiresXDGRuntimeDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", "")

	if _, err := NewPaths(); err == nil {
		t.Fatal("expected XDG_RUNTIME_DIR to be required")
	}
}

func TestNewPathsUsesTobySandboxRootOverride(t *testing.T) {
	home := t.TempDir()
	sandboxRoot := filepath.Join(home, "Sandboxes")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "IgnoredCache"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(home, "Runtime"))
	t.Setenv("TOBY_SANDBOX_ROOT", sandboxRoot)

	paths, err := NewPaths()
	if err != nil {
		t.Fatal(err)
	}
	if paths.SandboxRoot != sandboxRoot {
		t.Fatalf("SandboxRoot = %q, want %q", paths.SandboxRoot, sandboxRoot)
	}
}
