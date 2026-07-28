//go:build linux

package hostconfig

// Covers symlink-following host reads, regular-file overlay replacement, and
// required versus optional inputs.

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"petris.dev/toby/internal/sandbox/bwrap"
)

func TestCopyFilesFollowsSourcesAndReplacesOverlayEntries(t *testing.T) {
	directories := newRunDirectories(t)
	sourceRoot := t.TempDir()

	resolved := filepath.Join(sourceRoot, "resolved")
	if err := os.WriteFile(resolved, []byte("nameserver 192.0.2.1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(sourceRoot, "resolv.conf")
	if err := os.Symlink(resolved, source); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(directories.Overlay().Upper, "etc", "resolv.conf")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/outside", target); err != nil {
		t.Fatal(err)
	}

	if err := copyFiles(directories, []fileSpec{{
		source: source,
		target: "etc/resolv.conf",
	}}); err != nil {
		t.Fatal(err)
	}

	info, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("target mode = %s, want regular file", info.Mode())
	}
	if got, err := os.ReadFile(target); err != nil {
		t.Fatal(err)
	} else if want := "nameserver 192.0.2.1\n"; string(got) != want {
		t.Fatalf("target contents = %q, want %q", got, want)
	}
	if got := info.Mode().Perm(); got != copiedFileMode {
		t.Fatalf("target mode = %04o, want %04o", got, copiedFileMode)
	}
}

func TestCopyFilesAllowsMissingOptionalSource(t *testing.T) {
	directories := newRunDirectories(t)

	if err := copyFiles(directories, []fileSpec{{
		source:   filepath.Join(t.TempDir(), "missing"),
		target:   "etc/optional",
		optional: true,
	}}); err != nil {
		t.Fatal(err)
	}
}

func TestCopyFilesRequiresResolverSource(t *testing.T) {
	directories := newRunDirectories(t)
	source := filepath.Join(t.TempDir(), "missing")

	err := copyFiles(directories, []fileSpec{{
		source: source,
		target: "etc/resolv.conf",
	}})
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("error = %v, want missing source", err)
	}
}

func newRunDirectories(t *testing.T) *bwrap.RunDirectories {
	t.Helper()

	store, err := bwrap.OpenRunStorage(
		filepath.Join(t.TempDir(), "runs"),
		bwrap.DefaultRunStorageLimits(),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})

	directories, err := store.Create(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := directories.Close(); err != nil {
			t.Error(err)
		}
	})

	return directories
}
