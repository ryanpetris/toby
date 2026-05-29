package control

import (
	"path/filepath"
	"testing"
)

func TestDefaultControlPathDefaultsToLocalState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", "")

	got, err := DefaultControlPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".local", "state", "toby", "control")
	if got != want {
		t.Fatalf("DefaultControlPath = %q, want %q", got, want)
	}
}

func TestDefaultControlPathUsesXDGStateHome(t *testing.T) {
	home := t.TempDir()
	stateHome := filepath.Join(home, "State")
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", stateHome)

	got, err := DefaultControlPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(stateHome, "toby", "control")
	if got != want {
		t.Fatalf("DefaultControlPath = %q, want %q", got, want)
	}
}
