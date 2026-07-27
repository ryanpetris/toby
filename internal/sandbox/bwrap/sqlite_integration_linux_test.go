//go:build linux

package bwrap

// Exercises SQLite WAL locking through two independent Bubblewrap runs that
// share one native private home without a Toby application lock.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/sandbox/mount"
)

const defaultSQLiteOCIReference = "docker.io/nouchka/sqlite3:latest"

func TestBubblewrapIndependentRunsShareSQLiteWAL(t *testing.T) {
	if os.Getenv("TOBY_BWRAP_SQLITE_INTEGRATION") != "1" {
		t.Skip(
			"set TOBY_BWRAP_SQLITE_INTEGRATION=1 on a Linux host with Bubblewrap and user namespaces",
		)
	}
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skipf("Bubblewrap is unavailable: %v", err)
	}

	reference := os.Getenv("TOBY_BWRAP_SQLITE_OCI_REFERENCE")
	if reference == "" {
		reference = defaultSQLiteOCIReference
	}
	fixture := prepareVerticalFixtureWithManaged(
		t,
		reference,
		false,
		mount.Request{
			Key: mount.Key{
				Type:    mount.TypeTool,
				Name:    "opencode",
				Purpose: "data",
			},
			Target: layout.Home + "/.local/share/opencode",
			Access: mount.AccessRegular,
		},
	)
	second := secondSQLiteRun(t, fixture)

	sqlitePath := "/usr/bin/sqlite3"
	var version bytes.Buffer
	code, err := fixture.run.Execute(t.Context(), Command{
		Argv:         []string{sqlitePath, "-version"},
		Mode:         ExecutionNonInteractive,
		Capabilities: CapabilityDropAll,
	}, ProcessIO{
		Stdout: &version,
		Stderr: &version,
	})
	if err != nil {
		t.Fatalf("probe SQLite in Bubblewrap: %v", err)
	}
	if code != 0 {
		t.Skipf(
			"SQLite is unavailable in OCI image %q: code=%d output=%s",
			reference,
			code,
			strings.TrimSpace(version.String()),
		)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	database := fixture.managed.Entry().Target + "/opencode.db"
	hostDatabase := filepath.Join(
		fixture.managed.Entry().HostPath,
		"opencode.db",
	)
	if output, err := executeSQLite(
		ctx,
		fixture.run,
		sqlitePath,
		database,
		bwrapSQLiteSchemaFixture,
	); err != nil {
		t.Fatalf("initialize SQLite WAL database: %v: %s", err, output)
	} else if strings.TrimSpace(output) != "wal" {
		t.Fatalf("SQLite journal mode = %q, want wal", strings.TrimSpace(output))
	}

	hostInfo, err := os.Stat(hostDatabase)
	if err != nil {
		t.Fatal(err)
	}
	hostStat, ok := hostInfo.Sys().(*syscall.Stat_t)
	if !ok || hostStat == nil {
		t.Fatal("host SQLite filesystem identity is unavailable")
	}
	wantIdentity := fmt.Sprintf("%d:%d", hostStat.Dev, hostStat.Ino)
	for name, run := range map[string]*Run{
		"first run":  fixture.run,
		"second run": second,
	} {
		identity, err := sandboxFileIdentity(ctx, run, database)
		if err != nil {
			t.Fatalf("%s SQLite identity: %v", name, err)
		}
		if identity != wantIdentity {
			t.Fatalf(
				"%s SQLite identity = %q, want host identity %q",
				name,
				identity,
				wantIdentity,
			)
		}
	}

	firstScript := bwrapSQLiteFirstWriterFixture
	secondScript := bwrapSQLiteSecondWriterFixture

	type sqliteResult struct {
		output string
		err    error
	}
	firstResult := make(chan sqliteResult, 1)
	go func() {
		output, err := executeSQLite(
			ctx,
			fixture.run,
			sqlitePath,
			database,
			firstScript,
		)
		firstResult <- sqliteResult{output: output, err: err}
	}()
	waitForSQLiteMarker(
		t,
		ctx,
		filepath.Join(fixture.home.HostPath(), "first-locked"),
	)
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(hostDatabase + suffix); err != nil {
			t.Fatalf("SQLite WAL sidecar %q is unavailable: %v", suffix, err)
		}
	}

	secondResult := make(chan sqliteResult, 1)
	go func() {
		output, err := executeSQLite(
			ctx,
			second,
			sqlitePath,
			database,
			secondScript,
		)
		secondResult <- sqliteResult{output: output, err: err}
	}()
	waitForSQLiteMarker(
		t,
		ctx,
		filepath.Join(fixture.home.HostPath(), "second-started"),
	)

	for name, resultChannel := range map[string]<-chan sqliteResult{
		"first run":  firstResult,
		"second run": secondResult,
	} {
		select {
		case result := <-resultChannel:
			if result.err != nil {
				t.Errorf(
					"%s SQLite transaction: %v: %s",
					name,
					result.err,
					strings.TrimSpace(result.output),
				)
			}
		case <-ctx.Done():
			t.Fatalf("%s SQLite transaction: %v", name, ctx.Err())
		}
	}
	if t.Failed() {
		return
	}

	output, err := executeSQLite(
		ctx,
		second,
		sqlitePath,
		database,
		bwrapSQLiteVerifyFixture,
	)
	if err != nil {
		t.Fatalf("verify shared SQLite database: %v: %s", err, output)
	}
	lines := strings.Fields(output)
	if len(lines) != 3 ||
		lines[0] != "wal" ||
		lines[1] != "ok" ||
		lines[2] != "200|2|10100" {
		t.Fatalf(
			"SQLite verification output = %q, want WAL, integrity ok, and both transactions",
			output,
		)
	}
}

func secondSQLiteRun(t *testing.T, fixture *verticalFixture) *Run {
	t.Helper()

	runStorage, err := OpenRunStorage(
		fixture.run.plan.Overlay.RunStorageDir,
		RunStorageLimits{},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runStorage.Close(); err != nil {
			t.Error(err)
		}
	})

	directories, err := runStorage.Create(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	transferred := false
	t.Cleanup(func() {
		if !transferred {
			if err := directories.Close(); err != nil {
				t.Error(err)
			}
		}
	})

	plan := fixture.run.Plan()
	plan.RunID = directories.ID()
	plan.Overlay = directories.Overlay()

	sources, err := fixture.run.sources.current()
	if err != nil {
		t.Fatal(err)
	}
	sources.OverlayUpper, err = directories.UpperFile()
	if err != nil {
		t.Fatal(err)
	}
	defer sources.OverlayUpper.Close()
	sources.OverlayWork, err = directories.WorkFile()
	if err != nil {
		t.Fatal(err)
	}
	defer sources.OverlayWork.Close()

	run, err := NewRun(plan, sources, directories, fixture.run.executor, nil)
	if err != nil {
		t.Fatal(err)
	}
	transferred = true
	t.Cleanup(func() {
		if err := run.Close(); err != nil {
			t.Error(err)
		}
	})

	return run
}

func sandboxFileIdentity(
	ctx context.Context,
	run *Run,
	path string,
) (string, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code, err := run.Execute(ctx, Command{
		Argv: []string{
			"/bin/sh",
			"-c",
			`stat -c '%d:%i' "$1"`,
			"sqlite-identity",
			path,
		},
		Mode:         ExecutionNonInteractive,
		Capabilities: CapabilityDropAll,
	}, ProcessIO{
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		return "", sqliteDiagnostic(err, stderr.String())
	}
	if code != 0 {
		return "", sqliteDiagnostic(
			fmt.Errorf("stat exited with status %d", code),
			stderr.String(),
		)
	}

	return strings.TrimSpace(stdout.String()), nil
}

func executeSQLite(
	ctx context.Context,
	run *Run,
	executable string,
	database string,
	script string,
) (string, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	shellScript := bwrapSQLiteShellPrefixFixture + script + bwrapSQLiteShellSuffixFixture
	code, err := run.Execute(ctx, Command{
		Argv: []string{
			"/bin/sh",
			"-c",
			shellScript,
			"sqlite-script",
			executable,
			database,
		},
		Mode:         ExecutionNonInteractive,
		Capabilities: CapabilityDropAll,
	}, ProcessIO{
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		return stdout.String(), sqliteDiagnostic(err, stderr.String())
	}
	if code != 0 {
		return stdout.String(), sqliteDiagnostic(
			fmt.Errorf("sqlite3 exited with status %d", code),
			stderr.String(),
		)
	}

	return stdout.String(), nil
}

func sqliteDiagnostic(err error, stderr string) error {
	diagnostic := strings.TrimSpace(stderr)
	if diagnostic == "" {
		return err
	}

	return fmt.Errorf("%w: %s", err, diagnostic)
}

func waitForSQLiteMarker(
	t *testing.T,
	ctx context.Context,
	path string,
) {
	t.Helper()

	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("inspect SQLite coordination marker: %v", err)
		}

		select {
		case <-ctx.Done():
			t.Fatalf("wait for SQLite coordination marker %q: %v", path, ctx.Err())
		case <-ticker.C:
		}
	}
}
