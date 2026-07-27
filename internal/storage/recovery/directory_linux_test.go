//go:build linux

package recovery

// Tests bounded abandoned-directory cleanup and live-publication exclusion.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"petris.dev/toby/internal/storage/safefs"
)

func TestCleanupTemporaryDirectoriesRemovesRestrictiveStaleTree(t *testing.T) {
	directory, path := testRecoveryDirectory(t)
	name := ".toby-tmp-0123456789abcdef0123456789abcdef"
	nested := filepath.Join(path, name, "nested")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "file"), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(nested, 0); err != nil {
		t.Fatal(err)
	}

	if err := CleanupTemporaryDirectories(directory, 10, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(path, name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale publication remains: %v", err)
	}
}

func TestCleanupTemporaryDirectoriesRemovesRestrictiveStaleRoot(t *testing.T) {
	directory, path := testRecoveryDirectory(t)
	name := ".toby-tmp-0123456789abcdef0123456789abcdef"
	temporaryPath := filepath.Join(path, name)
	if err := os.Mkdir(temporaryPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(temporaryPath, 0); err != nil {
		t.Fatal(err)
	}

	if err := CleanupTemporaryDirectories(directory, 10, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(temporaryPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restrictive stale publication root remains: %v", err)
	}
}

func TestCleanupTemporaryDirectoriesSkipsLivePublisher(t *testing.T) {
	directory, path := testRecoveryDirectory(t)
	started := make(chan struct{})
	release := make(chan struct{})

	type result struct {
		published bool
		err       error
	}
	resultChannel := make(chan result, 1)
	go func() {
		published, err := directory.PublishDirectory("final", 10, func(stage *safefs.Directory) error {
			close(started)
			<-release
			return stage.WriteFile("value", []byte("published"), 0o600)
		})
		resultChannel <- result{published: published, err: err}
	}()

	<-started
	stale := ".toby-tmp-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := os.Mkdir(filepath.Join(path, stale), 0o700); err != nil {
		close(release)
		t.Fatal(err)
	}
	if err := CleanupTemporaryDirectories(directory, 10, 10); err != nil {
		close(release)
		t.Fatal(err)
	}
	if count := countTemporaryDirectories(t, path); count != 1 {
		close(release)
		t.Fatalf("temporary directories after recovering beside a live publisher = %d, want 1", count)
	}
	if _, err := os.Lstat(filepath.Join(path, stale)); !errors.Is(err, os.ErrNotExist) {
		close(release)
		t.Fatalf("stale temporary directory remains beside live publisher: %v", err)
	}

	close(release)
	outcome := <-resultChannel
	if outcome.err != nil {
		t.Fatal(outcome.err)
	}
	if !outcome.published {
		t.Fatal("live publisher lost its publication")
	}
	data, err := os.ReadFile(filepath.Join(path, "final", "value"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "published" {
		t.Fatalf("published data = %q", data)
	}
}

func TestCleanupTemporaryDirectoriesSkipsUnprotectedCandidateDuringPublication(t *testing.T) {
	directory, path := testRecoveryDirectory(t)
	unlock := retainSharedParentLock(t, path)
	name := ".toby-tmp-0123456789abcdef0123456789abcdef"
	temporaryPath := filepath.Join(path, name)
	if err := os.Mkdir(temporaryPath, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := CleanupTemporaryDirectories(directory, 10, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(temporaryPath); err != nil {
		t.Fatalf("candidate created before its inode lock was removed: %v", err)
	}

	unlock()
	if err := CleanupTemporaryDirectories(directory, 10, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(temporaryPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("candidate remains after publisher released parent: %v", err)
	}
}

func TestCleanupTemporaryDirectoriesMakesBoundedProgress(t *testing.T) {
	directory, path := testRecoveryDirectory(t)
	for _, name := range []string{
		".toby-tmp-0123456789abcdef0123456789abcdef",
		".toby-tmp-fedcba9876543210fedcba9876543210",
	} {
		if err := os.Mkdir(filepath.Join(path, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	if err := CleanupTemporaryDirectories(directory, 1, 1); !errors.Is(err, safefs.ErrLimitExceeded) {
		t.Fatalf("first cleanup error = %v, want ErrLimitExceeded", err)
	}
	if count := countTemporaryDirectories(t, path); count != 1 {
		t.Fatalf("temporary directories after first cleanup = %d, want 1", count)
	}
	if err := CleanupTemporaryDirectories(directory, 1, 1); err != nil {
		t.Fatal(err)
	}
	if count := countTemporaryDirectories(t, path); count != 0 {
		t.Fatalf("temporary directories after second cleanup = %d, want 0", count)
	}
}

func TestCleanupTemporaryDirectoriesRejectsRecognizedSymlink(t *testing.T) {
	directory, path := testRecoveryDirectory(t)
	name := ".toby-tmp-0123456789abcdef0123456789abcdef"
	outside := filepath.Join(filepath.Dir(path), filepath.Base(path)+"-outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		os.RemoveAll(outside)
	})
	if err := os.Symlink(outside, filepath.Join(path, name)); err != nil {
		t.Fatal(err)
	}

	if err := CleanupTemporaryDirectories(directory, 10, 10); !errors.Is(err, safefs.ErrUnsafePath) {
		t.Fatalf("cleanup error = %v, want ErrUnsafePath", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside directory changed: %v", err)
	}
}

func countTemporaryDirectories(t *testing.T, path string) int {
	t.Helper()

	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() && isTemporaryName(entry.Name()) {
			count++
		}
	}
	return count
}
