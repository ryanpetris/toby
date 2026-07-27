//go:build linux

package storage

// Provides an opt-in external SQLite WAL integration check against shared
// native managed-directory storage.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"petris.dev/toby/internal/sandbox/mount"
)

func TestSQLiteWALAcrossManagedDirectoryHandles(t *testing.T) {
	if os.Getenv("TOBY_TEST_SQLITE3") != "1" {
		t.Skip("set TOBY_TEST_SQLITE3=1 to run the external sqlite3 integration test")
	}
	sqlite, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skipf("sqlite3 is unavailable: %v", err)
	}

	base := secureStorageTestPath(t)
	services := []*Store{
		newStorageTestStore(t, base),
		newStorageTestStore(t, base),
	}
	managed := make([]*ManagedHandle, len(services))
	request := mount.Request{
		Key:    mount.Key{Type: mount.TypeTool, Name: "opencode", Purpose: "data"},
		Target: "~/.local/share/opencode",
	}
	for index, service := range services {
		managed[index] = resolveOneManaged(
			t,
			service,
			ProfileSelection{},
			request,
			SeedSource{},
		)
		defer managed[index].Close()
	}
	assertSameDirectory(t, managed[0].Entry().HostPath, managed[1].Entry().HostPath)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	database := filepath.Join(managed[0].Entry().HostPath, "opencode.db")
	if _, err := runSQLite(ctx, sqlite, database, storageSQLiteSchemaFixture); err != nil {
		t.Fatal(err)
	}

	const workers = 8
	const rowsPerWorker = 100
	errorsByWorker := make(chan error, workers)
	var wait sync.WaitGroup
	for worker := range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()

			path := filepath.Join(managed[worker%len(managed)].Entry().HostPath, "opencode.db")
			_, err := runSQLite(ctx, sqlite, path, fmt.Sprintf(storageSQLiteWorkerFixture, rowsPerWorker, worker))
			errorsByWorker <- err
		}()
	}
	wait.Wait()
	close(errorsByWorker)
	for err := range errorsByWorker {
		if err != nil {
			t.Error(err)
		}
	}
	if t.Failed() {
		return
	}

	output, err := runSQLite(ctx, sqlite, database, storageSQLiteVerifyFixture)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Fields(output)
	wantRows := fmt.Sprintf("%d", workers*rowsPerWorker)
	if len(lines) != 2 || lines[0] != "ok" || lines[1] != wantRows {
		t.Fatalf("SQLite verification output = %q, want integrity ok and %s rows", output, wantRows)
	}
}

func assertSameDirectory(t *testing.T, first, second string) {
	t.Helper()

	firstInfo, err := os.Stat(first)
	if err != nil {
		t.Fatal(err)
	}
	secondInfo, err := os.Stat(second)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(firstInfo, secondInfo) {
		t.Fatalf("%q and %q are different directories", first, second)
	}
}

func runSQLite(
	ctx context.Context,
	executable string,
	database string,
	statement string,
) (string, error) {
	command := exec.CommandContext(ctx, executable, "-batch", "-noheader", database, statement)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("sqlite3 %q: %w: %s", database, err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}
