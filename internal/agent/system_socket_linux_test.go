//go:build linux

package agent

// Exercises deterministic system agent socket path construction.

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSystemAgentSocketForUser(t *testing.T) {
	path, err := systemAgentSocketForUser("developer")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(
		systemAgentSocketRoot,
		"developer",
		"toby",
		"agent.sock",
	); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

func TestSystemAgentSocketRejectsPathSeparators(t *testing.T) {
	if _, err := systemAgentSocketForUser("team/developer"); err == nil {
		t.Fatal("accepted user name containing a path separator")
	}
}

func TestFirstAgentSocketPrefersPrimaryWhenBothExist(t *testing.T) {
	directory := t.TempDir()
	primary := filepath.Join(directory, "primary.sock")
	fallback := filepath.Join(directory, "fallback.sock")
	for _, path := range []string{primary, fallback} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	path, fallbackSelected := firstAgentSocket(primary, fallback)
	if path != primary || fallbackSelected {
		t.Fatalf(
			"selection = (%q, %v), want (%q, false)",
			path,
			fallbackSelected,
			primary,
		)
	}
}

func TestFirstAgentSocketUsesFallbackWhenPrimaryIsMissing(t *testing.T) {
	directory := t.TempDir()
	primary := filepath.Join(directory, "primary.sock")
	fallback := filepath.Join(directory, "fallback.sock")
	if err := os.WriteFile(fallback, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	path, fallbackSelected := firstAgentSocket(primary, fallback)
	if path != fallback || !fallbackSelected {
		t.Fatalf(
			"selection = (%q, %v), want (%q, true)",
			path,
			fallbackSelected,
			fallback,
		)
	}
}

func TestFirstAgentSocketUsesPrimaryWhenBothAreMissing(t *testing.T) {
	directory := t.TempDir()
	primary := filepath.Join(directory, "primary.sock")
	fallback := filepath.Join(directory, "fallback.sock")

	path, fallbackSelected := firstAgentSocket(primary, fallback)
	if path != primary || fallbackSelected {
		t.Fatalf(
			"selection = (%q, %v), want (%q, false)",
			path,
			fallbackSelected,
			primary,
		)
	}
}
