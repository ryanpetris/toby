//go:build linux

package safefs

// Tests shared, exclusive, and non-blocking flock behavior.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLockContentionAndRelease(t *testing.T) {
	directory, path := testDirectory(t)

	first, err := directory.Lock("object.lock", LockExclusive, false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := directory.Lock("object.lock", LockExclusive, true)
	if !errors.Is(err, ErrWouldBlock) {
		t.Fatalf("second exclusive lock error = %v, want ErrWouldBlock", err)
	}
	if second != nil {
		second.Close()
		t.Fatal("contending lock returned a handle")
	}

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err = directory.Lock("object.lock", LockExclusive, true)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	info, err := os.Stat(filepath.Join(path, "object.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("lock file mode = %04o, want 0600", info.Mode().Perm())
	}
}

func TestSharedLocksCoexistAndExcludeWriter(t *testing.T) {
	directory, _ := testDirectory(t)

	first, err := directory.Lock("shared.lock", LockShared, false)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := directory.Lock("shared.lock", LockShared, true)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	exclusive, err := directory.Lock("shared.lock", LockExclusive, true)
	if !errors.Is(err, ErrWouldBlock) {
		t.Fatalf("exclusive lock error = %v, want ErrWouldBlock", err)
	}
	if exclusive != nil {
		exclusive.Close()
		t.Fatal("exclusive lock unexpectedly succeeded")
	}
}

func TestLockRejectsSymlinkAndInvalidModeButAcceptsExistingPermissions(t *testing.T) {
	directory, path := testDirectory(t)
	outside := filepath.Join(filepath.Dir(path), filepath.Base(path)+"-lock-outside")
	if err := os.WriteFile(outside, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		os.Remove(outside)
	})
	if err := os.Symlink(outside, filepath.Join(path, "link.lock")); err != nil {
		t.Fatal(err)
	}
	if _, err := directory.Lock("link.lock", LockExclusive, true); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("symlink lock error = %v, want ErrUnsafePath", err)
	}

	if err := os.WriteFile(filepath.Join(path, "mode.lock"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	lock, err := directory.Lock("mode.lock", LockExclusive, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(path, "mode.lock")); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o644 {
		t.Fatalf("existing lock mode = %04o, want preserved 0644", info.Mode().Perm())
	}
	if _, err := directory.Lock("invalid.lock", LockMode(99), true); err == nil {
		t.Fatal("invalid lock mode was accepted")
	}
}

func TestDirectorySelfLocksCoordinateAndReleaseIndependently(t *testing.T) {
	directory, path := testDirectory(t)

	second, err := openTestRoot(path, testDirectoryOptions(os.Getuid()))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	shared, err := directory.LockSelf(LockShared, false)
	if err != nil {
		t.Fatal(err)
	}
	exclusive, err := second.LockSelf(LockExclusive, true)
	if !errors.Is(err, ErrWouldBlock) {
		t.Fatalf("exclusive lock error = %v, want ErrWouldBlock", err)
	}
	if exclusive != nil {
		exclusive.Close()
		t.Fatal("exclusive directory lock unexpectedly succeeded")
	}

	if err := shared.Close(); err != nil {
		t.Fatal(err)
	}
	exclusive, err = second.LockSelf(LockExclusive, true)
	if err != nil {
		t.Fatal(err)
	}
	defer exclusive.Close()

	if _, err := directory.Names(1); err != nil {
		t.Fatalf("directory capability was closed with its lock: %v", err)
	}
}
