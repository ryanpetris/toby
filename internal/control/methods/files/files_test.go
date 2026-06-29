package files

// Tests for sandbox-side file control handlers.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"petris.dev/toby/internal/control"
)

func TestFileMkdirCreatesOwnedParents(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "a", "b", "c")
	uid, gid := os.Getuid(), os.Getgid()

	if err := fileMkdir(MkdirParams{Path: target, Mode: 0o750, UID: uid, GID: gid}); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{filepath.Join(root, "a"), filepath.Join(root, "a", "b"), target} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		stat := info.Sys().(*syscall.Stat_t)
		if int(stat.Uid) != uid || int(stat.Gid) != gid {
			t.Fatalf("%s owner = %d:%d, want %d:%d", dir, stat.Uid, stat.Gid, uid, gid)
		}
	}
}

func TestFileCreateRoundTripsThroughRouter(t *testing.T) {
	router, err := control.NewRouter([]control.Capability{New()})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "nested", "file.txt")
	params, err := json.Marshal(CreateParams{Path: target, Data: []byte("hello"), Mode: 0o640, UID: os.Getuid(), GID: os.Getgid()})
	if err != nil {
		t.Fatal(err)
	}
	data, err := control.NewRequest(1, MethodCreate, params)
	if err != nil {
		t.Fatal(err)
	}
	req, err := control.DecodeRequest(data)
	if err != nil {
		t.Fatal(err)
	}

	response, err := router.Handle(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := control.DecodeResponse(response)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Error != nil {
		t.Fatalf("response error = %v", decoded.Error)
	}
	contents, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "hello" {
		t.Fatalf("contents = %q", contents)
	}
}
