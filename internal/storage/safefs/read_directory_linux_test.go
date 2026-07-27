//go:build linux

package safefs

// Tests bounded, repeatable direct-child enumeration.

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestNamesAreSortedAndUseIndependentStreams(t *testing.T) {
	directory, path := testDirectory(t)
	for _, name := range []string{"charlie", "alpha", "bravo"} {
		if err := os.WriteFile(filepath.Join(path, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	want := []string{"alpha", "bravo", "charlie"}
	for range 2 {
		got, err := directory.Names(3)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("Names() = %q, want %q", got, want)
		}
	}
}

func TestNamesEnforcesEntryLimit(t *testing.T) {
	directory, path := testDirectory(t)
	for _, name := range []string{"one", "two"} {
		if err := os.WriteFile(filepath.Join(path, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := directory.Names(1); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("Names() error = %v, want ErrLimitExceeded", err)
	}
}
