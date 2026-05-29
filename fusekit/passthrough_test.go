package fusekit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	gofuse "github.com/hanwen/go-fuse/v2/fuse"
)

func TestPassthroughMapsVirtualPathToSource(t *testing.T) {
	ctx := context.Background()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "file"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	mount, err := NewPassthroughMount(PassthroughOptions{ID: "p", BasePath: "/base", Source: source})
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter([]Mount{mount})
	if err != nil {
		t.Fatal(err)
	}
	res, err := router.Dispatch(ctx, Operation{Kind: OpOpen, Path: "/base/file", Flags: syscall.O_RDONLY})
	if err != nil {
		t.Fatal(err)
	}
	data, err := res.Handle.(FileReader).Read(ctx, make([]byte, 5), 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("read = %q, want hello", data)
	}
	if err := res.Handle.(FileReleaser).Release(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPassthroughMissingSourceReturnsENOENTAtRuntime(t *testing.T) {
	mount, err := NewPassthroughMount(PassthroughOptions{ID: "p", BasePath: "/", Source: filepath.Join(t.TempDir(), "missing")})
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter([]Mount{mount})
	if err != nil {
		t.Fatal(err)
	}
	_, err = router.Dispatch(context.Background(), Operation{Kind: OpGetAttr, Path: "/"})
	if !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("err = %v, want ENOENT", err)
	}
}

func TestPassthroughMetadataIncludesMountIDAndHostIdentity(t *testing.T) {
	ctx := context.Background()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "file"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	mount, err := NewPassthroughMount(PassthroughOptions{ID: "p", BasePath: "/", Source: source})
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter([]Mount{mount})
	if err != nil {
		t.Fatal(err)
	}
	res, err := router.Dispatch(ctx, Operation{Kind: OpGetAttr, Path: "/file"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Attr.Object.MountID != "p" || res.Attr.Object.Kind != "passthrough" || res.Attr.Size != 5 {
		t.Fatalf("attr = %#v", res.Attr)
	}
	if res.Attr.Mode&0o777 != 0o600 {
		t.Fatalf("mode = %#o, want 0600", res.Attr.Mode&0o777)
	}
}

func TestPassthroughReadOnlyDeniesMutations(t *testing.T) {
	ctx := context.Background()
	source := t.TempDir()
	mount, err := NewPassthroughMount(PassthroughOptions{ID: "p", BasePath: "/", Source: source, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter([]Mount{mount})
	if err != nil {
		t.Fatal(err)
	}
	mutations := []Operation{
		{Kind: OpCreate, Path: "/file", Flags: syscall.O_RDWR, Mode: 0o644},
		{Kind: OpMkdir, Path: "/dir", Mode: 0o755},
		{Kind: OpUnlink, Path: "/file"},
		{Kind: OpRmdir, Path: "/dir"},
		{Kind: OpRename, OldPath: "/a", NewPath: "/b"},
		{Kind: OpSetAttr, Path: "/"},
		{Kind: OpMaterialize, Path: "/parent"},
	}
	for _, op := range mutations {
		_, err := router.Dispatch(ctx, op)
		if ErrnoOf(err) != syscall.EROFS {
			t.Fatalf("%s err = %v, want EROFS", op.Kind, err)
		}
	}
}

func TestPassthroughWritableCreateReadWriteRemove(t *testing.T) {
	ctx := context.Background()
	source := t.TempDir()
	mount, err := NewPassthroughMount(PassthroughOptions{ID: "p", BasePath: "/", Source: source})
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter([]Mount{mount})
	if err != nil {
		t.Fatal(err)
	}
	res, err := router.Dispatch(ctx, Operation{Kind: OpCreate, Path: "/file", Flags: syscall.O_RDWR, Mode: 0o644})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := res.Handle.(FileWriter).Write(ctx, []byte("hello"), 0); err != nil {
		t.Fatal(err)
	}
	data, err := res.Handle.(FileReader).Read(ctx, make([]byte, 5), 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("read = %q, want hello", data)
	}
	if err := res.Handle.(FileReleaser).Release(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := router.Dispatch(ctx, Operation{Kind: OpUnlink, Path: "/file"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(source, "file")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file still exists or unexpected error: %v", err)
	}
}

func TestPassthroughOpenStripsFuseOnlyFlags(t *testing.T) {
	ctx := context.Background()
	source := t.TempDir()
	path := filepath.Join(source, "script")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	mount, err := NewPassthroughMount(PassthroughOptions{ID: "p", BasePath: "/", Source: source})
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter([]Mount{mount})
	if err != nil {
		t.Fatal(err)
	}
	res, err := router.Dispatch(ctx, Operation{Kind: OpOpen, Path: "/script", Flags: syscall.O_RDONLY | gofuse.FMODE_EXEC})
	if err != nil {
		t.Fatal(err)
	}
	data, err := res.Handle.(FileReader).Read(ctx, make([]byte, 10), 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "#!/bin/sh\n" {
		t.Fatalf("read = %q", data)
	}
	if err := res.Handle.(FileReleaser).Release(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPassthroughOpenRejectsSpecialFilesWithoutBlocking(t *testing.T) {
	ctx := context.Background()
	source := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(source, "pipe"), 0o644); err != nil {
		t.Fatal(err)
	}
	mount, err := NewPassthroughMount(PassthroughOptions{ID: "p", BasePath: "/", Source: source})
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter([]Mount{mount})
	if err != nil {
		t.Fatal(err)
	}
	_, err = router.Dispatch(ctx, Operation{Kind: OpOpen, Path: "/pipe", Flags: syscall.O_RDONLY})
	if ErrnoOf(err) != syscall.EOPNOTSUPP {
		t.Fatalf("open fifo err = %v, want EOPNOTSUPP", err)
	}
}

func TestReadOnlyFlushDoesNotInvalidateInode(t *testing.T) {
	ctx := context.Background()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "file"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	mount, err := NewPassthroughMount(PassthroughOptions{ID: "p", BasePath: "/", Source: source})
	if err != nil {
		t.Fatal(err)
	}
	recorder := &RecordingInvalidator{}
	router, err := NewRouterWithOptions([]Mount{mount}, RouterOptions{Invalidator: recorder})
	if err != nil {
		t.Fatal(err)
	}
	res, err := router.Dispatch(ctx, Operation{Kind: OpOpen, Path: "/file", Flags: syscall.O_RDONLY})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := res.Handle.(FileReader).Read(ctx, make([]byte, 5), 0); err != nil {
		t.Fatal(err)
	}
	if err := res.Handle.(FileFlusher).Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if got := recorder.InodeEvents(); len(got) != 0 {
		t.Fatalf("inode invalidations = %#v, want none", got)
	}
	if err := res.Handle.(FileReleaser).Release(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPassthroughMaterializationCreatesDirectories(t *testing.T) {
	source := t.TempDir()
	mount, err := NewPassthroughMount(PassthroughOptions{ID: "p", BasePath: "/", Source: source})
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter([]Mount{mount})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := router.Dispatch(context.Background(), Operation{Kind: OpMaterialize, Path: "/a/b"}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(source, "a", "b")); err != nil || !info.IsDir() {
		t.Fatalf("materialized dir info = %#v err = %v", info, err)
	}
}
