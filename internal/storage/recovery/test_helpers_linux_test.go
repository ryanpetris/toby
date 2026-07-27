//go:build linux

package recovery

// Provides owner-controlled directory capabilities and parent locks for tests.

import (
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/storage/safefs"
)

func testRecoveryDirectory(
	t *testing.T,
) (*safefs.Directory, string) {
	t.Helper()

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	path, err := os.MkdirTemp(home, ".toby-recovery-test-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		os.RemoveAll(path)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(path); err != nil {
			t.Errorf("remove recovery test root: %v", err)
		}
	})

	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := safefs.OpenDirectoryFile(
		file,
		path,
		safefs.DirectoryOptions{
			OwnerUID: os.Getuid(),
			OwnerGID: os.Getgid(),
		},
	)
	if closeErr := file.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := directory.Close(); err != nil &&
			!strings.Contains(err.Error(), "file already closed") {
			t.Errorf("close recovery test root: %v", err)
		}
	})

	return directory, path
}

func retainSharedParentLock(t *testing.T, path string) func() {
	t.Helper()

	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_SH); err != nil {
		file.Close()
		t.Fatal(err)
	}

	released := false
	unlock := func() {
		t.Helper()

		if released {
			return
		}
		released = true
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(unlock)

	return unlock
}
