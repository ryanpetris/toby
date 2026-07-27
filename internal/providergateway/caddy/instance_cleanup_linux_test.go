//go:build linux

package caddy

// Verifies that generation ownership completes multi-batch run cleanup before
// publishing process completion.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"petris.dev/toby/internal/sandbox/bwrap"
)

func TestInstanceWaitCompletesBoundedRunDirectoryBatches(t *testing.T) {
	base, err := os.MkdirTemp(".", ".toby-caddy-cleanup-test-")
	if err != nil {
		t.Fatal(err)
	}
	absolute, err := filepath.Abs(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(absolute, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(absolute); err != nil {
			t.Error(err)
		}
	})

	storage, err := bwrap.OpenRunStorage(
		filepath.Join(absolute, "runs"),
		bwrap.RunStorageLimits{MaxCleanupEntries: 3},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := storage.Close(); err != nil {
			t.Error(err)
		}
	})
	directories, err := storage.Create(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	runtimePath := directories.RuntimePath()
	runRoot := filepath.Dir(runtimePath)
	for index := range 8 {
		name := fmt.Sprintf("entry-%d", index)
		if err := os.WriteFile(
			filepath.Join(runtimePath, name),
			[]byte(name),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
	}

	background := &stopTestBackground{done: make(chan struct{})}
	instance, err := newInstance(
		background,
		directories,
		filepath.Join(runtimePath, "admin.sock"),
		filepath.Join(runtimePath, "data.sock"),
		os.Geteuid(),
		os.Getegid(),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	close(background.done)

	select {
	case <-instance.Done():
	case <-time.After(time.Second):
		t.Fatal("Caddy generation did not finish bounded cleanup")
	}
	if err := instance.Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(runRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Caddy run remains after bounded cleanup: %v", err)
	}
}
