//go:build linux

package resourcelog

// Exercises exact resource paths, best-effort mode repair, and bounded
// retention.

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"petris.dev/toby/internal/agent/protocol"
	"petris.dev/toby/internal/config"
)

func TestServiceCreatesReopensAndRepairsResourceLog(t *testing.T) {
	service, paths := testService(t)
	resourceID := protocol.ResourceID("blake2b-512:abcd")
	operationID := protocol.OperationID("operation-one")

	file, err := service.Create(
		protocol.ResourceOCI,
		resourceID,
		operationID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{\"kind\":\"complete\"}\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(
		resourceLogsDir(paths),
		string(protocol.ResourceOCI),
		string(resourceID),
		string(operationID)+".jsonl",
	)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	opened, selected, err := service.Open(
		protocol.ResourceOCI,
		resourceID,
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	if selected != operationID {
		t.Fatalf("selected operation = %q, want %q", selected, operationID)
	}
	info, err := opened.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("log mode = %04o, want 0600", info.Mode().Perm())
	}
}

func TestServiceRetainsBoundedLogs(t *testing.T) {
	service, paths := testService(t)
	resourceID := protocol.ResourceID("blake2b-512:retention")

	for index := 0; index < retainedLogsPerResource+3; index++ {
		file, err := service.Create(
			protocol.ResourceMCP,
			resourceID,
			protocol.OperationID(fmt.Sprintf("operation-%03d", index)),
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}

	entries, err := os.ReadDir(filepath.Join(
		resourceLogsDir(paths),
		string(protocol.ResourceMCP),
		string(resourceID),
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != retainedLogsPerResource {
		t.Fatalf(
			"retained logs = %d, want %d",
			len(entries),
			retainedLogsPerResource,
		)
	}
}

func testService(t *testing.T) (*Service, config.Paths) {
	t.Helper()
	root := t.TempDir()
	paths := config.Paths{
		Home:         root,
		XDGCacheHome: filepath.Join(root, "cache"),
	}
	return NewService(paths, nil), paths
}

func resourceLogsDir(paths config.Paths) string {
	return filepath.Join(paths.TobyCacheDir(), "logs")
}
