//go:build linux

package bwrap

// Exercises unique overlay creation, exact teardown, live-run exclusion, and
// bounded interrupted-run recovery on Linux.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"petris.dev/toby/internal/storage/safefs"
)

func TestRunStorageCreatesUniqueSiblingOverlaysAndRemovesExactly(t *testing.T) {
	path := secureRunStorageTestPath(t)
	store, err := OpenRunStorage(path, RunStorageLimits{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})

	first, err := store.Create(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Create(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if first.ID() == second.ID() ||
		!generatedRunIDPattern.MatchString(first.ID()) ||
		!generatedRunIDPattern.MatchString(second.ID()) {
		t.Fatalf("run IDs are not unique generated identities: %q and %q", first.ID(), second.ID())
	}
	for _, run := range []*RunDirectories{first, second} {
		overlay := run.Overlay()
		if filepath.Dir(overlay.Upper) != filepath.Dir(overlay.Work) ||
			filepath.Base(overlay.Upper) != "upper" ||
			filepath.Base(overlay.Work) != "work" {
			t.Fatalf("invalid overlay pair: %#v", overlay)
		}
		assertRunDirectoryMode(t, overlay.Upper, 0o700)
		assertRunDirectoryMode(t, overlay.Work, 0o700)
	}

	secondRoot := filepath.Dir(second.Overlay().Upper)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(path, first.ID())); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("closed run remains present: %v", err)
	}
	if _, err := os.Stat(secondRoot); err != nil {
		t.Fatalf("closing first run affected second: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRunStorageFollowsSymlinkedTobyCacheRoot(t *testing.T) {
	base := secureRunStorageTestPath(t)
	cacheHome := filepath.Join(base, "cache")
	target := filepath.Join(base, "cache-target")
	if err := os.Mkdir(cacheHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cacheHome, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(cacheHome, "toby")); err != nil {
		t.Fatal(err)
	}
	runStorage := filepath.Join(cacheHome, "toby", "runs")

	store, err := OpenRunStorage(
		runStorage,
		RunStorageLimits{},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	run, err := store.Create(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer run.Close()
	if _, err := os.Stat(filepath.Join(target, "runs", run.ID())); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(cacheHome); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o750 {
		t.Fatalf("XDG cache parent mode = %04o, want 0750", info.Mode().Perm())
	}
	if info, err := os.Stat(target); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o700 {
		t.Fatalf("resolved Toby cache root mode = %04o, want 0700", info.Mode().Perm())
	}
	if info, err := os.Lstat(filepath.Join(cacheHome, "toby")); err != nil {
		t.Fatal(err)
	} else if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("Toby cache-root symlink was replaced")
	}
}

func TestRunStorageRootFileRetainsExactRoot(t *testing.T) {
	path := secureRunStorageTestPath(t)
	store, err := OpenRunStorage(path, RunStorageLimits{}, nil)
	if err != nil {
		t.Fatal(err)
	}

	file, err := store.RootFile()
	if err != nil {
		t.Fatal(errors.Join(err, store.Close()))
	}
	want, err := os.Stat(path)
	if err != nil {
		t.Fatal(errors.Join(err, file.Close(), store.Close()))
	}
	got, err := file.Stat()
	if err != nil || !os.SameFile(got, want) {
		t.Fatal(errors.Join(
			fmt.Errorf("run-storage descriptor is not exact: %v", err),
			file.Close(),
			store.Close(),
		))
	}

	if err := store.Close(); err != nil {
		t.Fatal(errors.Join(err, file.Close()))
	}
	if _, err := file.Stat(); err != nil {
		t.Fatal(errors.Join(
			fmt.Errorf(
				"caller-owned run-storage descriptor followed close: %w",
				err,
			),
			file.Close(),
		))
	}
	if _, err := store.RootFile(); err == nil {
		t.Fatal(errors.Join(
			errors.New("closed run storage returned a root descriptor"),
			file.Close(),
		))
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRunDirectoriesCloseCompletesBoundedCleanupBatches(t *testing.T) {
	path := secureRunStorageTestPath(t)
	store, err := OpenRunStorage(
		path,
		RunStorageLimits{MaxCleanupEntries: 3},
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

	run, err := store.Create(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	runRoot := filepath.Dir(run.Overlay().Upper)
	for index := range 8 {
		name := fmt.Sprintf("entry-%d", index)
		if err := os.WriteFile(
			filepath.Join(run.Overlay().Upper, name),
			[]byte(name),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
	}

	if err := run.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(runRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("run remains after bounded cleanup batches: %v", err)
	}
}

func TestRunDirectoriesCloseDefersIncompleteCleanupToRecovery(
	t *testing.T,
) {
	path := secureRunStorageTestPath(t)
	store, err := OpenRunStorage(
		path,
		RunStorageLimits{MaxCleanupEntries: 1},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	runRoot := filepath.Dir(run.Overlay().Upper)
	closeResult := make(chan error, 1)
	go func() {
		closeResult <- run.Close()
	}()
	select {
	case err = <-closeResult:
	case <-time.After(time.Second):
		t.Fatal("Close hung after bounded cleanup made no progress")
	}
	if err != nil {
		t.Fatalf("Close error = %v", err)
	}
	if _, err := os.Stat(runRoot); err != nil {
		t.Fatalf("deferred run cleanup is unavailable for recovery: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	recovered, err := OpenRunStorage(
		path,
		DefaultRunStorageLimits(),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := recovered.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := os.Stat(runRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("run remains after recovery: %v", err)
	}
}

func TestContainsOnlyErrorRejectsJoinedNonRetryableFailure(t *testing.T) {
	limitErr := fmt.Errorf("bounded cleanup: %w", safefs.ErrLimitExceeded)
	if !containsOnlyError(limitErr, safefs.ErrLimitExceeded) {
		t.Fatal("wrapped limit error was not retryable")
	}
	if containsOnlyError(
		errors.Join(limitErr, errors.New("close mutation lock")),
		safefs.ErrLimitExceeded,
	) {
		t.Fatal("joined non-retryable error was treated as retryable")
	}
}

func TestRunStorageConcurrentCreationNeverSharesAnOverlay(t *testing.T) {
	path := secureRunStorageTestPath(t)
	first, err := OpenRunStorage(path, RunStorageLimits{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := OpenRunStorage(path, RunStorageLimits{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	const count = 32
	results := make(chan *RunDirectories, count)
	failures := make(chan error, count)
	var wait sync.WaitGroup
	for index := range count {
		wait.Add(1)
		go func(store *RunStorage) {
			defer wait.Done()
			run, err := store.Create(t.Context())
			if err != nil {
				failures <- err
				return
			}
			results <- run
		}([]*RunStorage{first, second}[index%2])
	}
	wait.Wait()
	close(results)
	close(failures)

	for err := range failures {
		t.Error(err)
	}
	seen := make(map[string]struct{}, count)
	var runs []*RunDirectories
	for run := range results {
		upper := run.Overlay().Upper
		if _, found := seen[upper]; found {
			t.Errorf("multiple runs shared overlay %q", upper)
		}
		seen[upper] = struct{}{}
		runs = append(runs, run)
	}
	for _, run := range runs {
		if err := run.Close(); err != nil {
			t.Error(err)
		}
	}
	if len(runs) != count {
		t.Fatalf("created runs = %d, want %d", len(runs), count)
	}
}

func TestRunStorageRecoverySkipsLiveRunAndRemovesAbandonedRun(t *testing.T) {
	path := secureRunStorageTestPath(t)
	first, err := OpenRunStorage(path, RunStorageLimits{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	run, err := first.Create(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	runRoot := filepath.Dir(run.Overlay().Upper)

	second, err := OpenRunStorage(path, RunStorageLimits{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(runRoot); err != nil {
		t.Fatalf("live run was recovered: %v", err)
	}

	abandonRunForTest(t, run)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, err := OpenRunStorage(path, RunStorageLimits{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := recovered.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(runRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("abandoned run remains present: %v", err)
	}
}

func TestRunStorageRecoveryMakesBoundedProgress(t *testing.T) {
	path := secureRunStorageTestPath(t)
	root, err := openRunStorageTestRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	for index := range 3 {
		createAbandonedRunFixture(t, root, testRunID(index))
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}

	limits := RunStorageLimits{
		MaxRecoveryCandidates: 2,
		MaxCleanupEntries:     100,
	}
	store, err := OpenRunStorage(path, limits, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	remaining := countRunDirectories(t, path)
	if remaining >= 3 {
		t.Fatalf("bounded recovery made no progress: %d runs remain", remaining)
	}

	for range 4 {
		store, err := OpenRunStorage(path, limits, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		if countRunDirectories(t, path) == 0 {
			return
		}
	}
	t.Fatal("bounded run recovery did not finish")
}

func openRunStorageTestRoot(
	path string,
) (*safefs.Directory, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	return safefs.OpenDirectoryFile(
		file,
		path,
		safefs.DirectoryOptions{
			OwnerUID: os.Geteuid(),
			OwnerGID: os.Getegid(),
		},
	)
}

func secureRunStorageTestPath(t *testing.T) string {
	t.Helper()

	cacheRoot, err := os.MkdirTemp(".", ".toby-bwrap-cache-")
	if err != nil {
		t.Fatal(err)
	}
	absolute, err := filepath.Abs(cacheRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(absolute, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(absolute, "runs")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(absolute); err != nil {
			t.Error(err)
		}
	})

	return path
}

func assertRunDirectoryMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode().Perm() != want {
		t.Fatalf("%s mode = %v, want directory %04o", path, info.Mode(), want)
	}
}

func abandonRunForTest(t *testing.T, run *RunDirectories) {
	t.Helper()

	run.mu.Lock()
	defer run.mu.Unlock()
	if err := errors.Join(
		closeDirectory(&run.work),
		closeDirectory(&run.upper),
		closeDirectory(&run.root),
		closeLock(run.lifetime),
		run.parent.Close(),
	); err != nil {
		t.Fatal(err)
	}
	run.lifetime = nil
	run.parent = nil
	run.closed = true
}

func createAbandonedRunFixture(
	t *testing.T,
	root *safefs.Directory,
	id string,
) {
	t.Helper()

	published, err := root.PublishDirectory(id, 100, func(stage *safefs.Directory) error {
		upper, err := stage.MkdirAll("upper")
		if err != nil {
			return err
		}
		work, err := stage.MkdirAll("work")
		if err != nil {
			return errors.Join(err, upper.Close())
		}
		lock, err := stage.Lock(runLifetimeLockName, safefs.LockExclusive, false)
		return errors.Join(err, closeLock(lock), work.Close(), upper.Close())
	})
	if err != nil {
		t.Fatal(err)
	}
	if !published {
		t.Fatalf("abandoned run fixture %q already exists", id)
	}
}

func testRunID(index int) string {
	value := make([]byte, runIDRandomBytes)
	value[len(value)-1] = byte(index + 1)

	return "run-" + hexString(value)
}

func hexString(value []byte) string {
	const digits = "0123456789abcdef"
	result := make([]byte, len(value)*2)
	for index, current := range value {
		result[index*2] = digits[current>>4]
		result[index*2+1] = digits[current&0x0f]
	}
	return string(result)
}

func countRunDirectories(t *testing.T, path string) int {
	t.Helper()

	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		if generatedRunIDPattern.MatchString(entry.Name()) {
			count++
		}
	}
	return count
}
