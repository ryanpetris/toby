package control

import (
	"path/filepath"
	"testing"
)

func TestDefaultSocketPathRequiresXDGRuntimeDir(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	if _, err := DefaultSocketPath(); err == nil {
		t.Fatal("expected XDG_RUNTIME_DIR to be required")
	}
}

func TestDefaultSocketPathUsesXDGRuntimeDir(t *testing.T) {
	home := t.TempDir()
	runtimeDir := filepath.Join(home, "Runtime")
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	got, err := DefaultSocketPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(runtimeDir, "toby", "sandbox.sock")
	if got != want {
		t.Fatalf("DefaultSocketPath = %q, want %q", got, want)
	}
}

func TestDefaultContextDirUsesXDGRuntimeDir(t *testing.T) {
	home := t.TempDir()
	runtimeDir := filepath.Join(home, "Runtime")
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	got, err := DefaultContextDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(runtimeDir, "toby", "context")
	if got != want {
		t.Fatalf("DefaultContextDir = %q, want %q", got, want)
	}
}
