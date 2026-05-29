package fusekit

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
)

func TestRouterRoutesHighestPriorityMatchFirst(t *testing.T) {
	ctx := context.Background()
	var calls []string
	low := testMount("low", "/", func(ctx context.Context, op Operation, next Next) (Result, error) {
		calls = append(calls, "low")
		attr := testAttr("low")
		return Result{Attr: &attr}, nil
	})
	high := testMount("high", "/foo", func(ctx context.Context, op Operation, next Next) (Result, error) {
		calls = append(calls, "high")
		attr := testAttr("high")
		return Result{Attr: &attr}, nil
	})
	router, err := NewRouter([]Mount{low, high})
	if err != nil {
		t.Fatal(err)
	}
	res, err := router.Dispatch(ctx, Operation{Kind: OpGetAttr, Path: "/foo/a"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := calls, []string{"high"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %#v, want %#v", got, want)
	}
	if res.Attr.Object.MountID != "high" {
		t.Fatalf("result mount = %q, want high", res.Attr.Object.MountID)
	}
}

func TestRouterNextPassesToLowerPriorityMatch(t *testing.T) {
	ctx := context.Background()
	var calls []string
	low := testMount("low", "/", func(ctx context.Context, op Operation, next Next) (Result, error) {
		calls = append(calls, "low")
		attr := testAttr("low")
		return Result{Attr: &attr}, nil
	})
	high := testMount("high", "/foo", func(ctx context.Context, op Operation, next Next) (Result, error) {
		calls = append(calls, "high")
		return next(ctx, op)
	})
	router, err := NewRouter([]Mount{low, high})
	if err != nil {
		t.Fatal(err)
	}
	res, err := router.Dispatch(ctx, Operation{Kind: OpGetAttr, Path: "/foo/a"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := calls, []string{"high", "low"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %#v, want %#v", got, want)
	}
	if res.Attr.Object.MountID != "low" {
		t.Fatalf("result mount = %q, want low", res.Attr.Object.MountID)
	}
}

func TestRouterNoMatchingMountReturnsENOENT(t *testing.T) {
	router, err := NewRouter([]Mount{testMount("m", "/foo", nil)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = router.Dispatch(context.Background(), Operation{Kind: OpGetAttr, Path: "/bar"})
	if ErrnoOf(err) != syscall.ENOENT {
		t.Fatalf("err = %v, want ENOENT", err)
	}
}

func TestRouterRenameSameTopMountAndCrossMount(t *testing.T) {
	ctx := context.Background()
	var gotRename Operation
	root := MountAdapter{IDValue: "root", BasePathValue: "/", Rename: func(ctx context.Context, op Operation, next Next) (Result, error) {
		gotRename = op
		return Result{}, nil
	}}
	other := MountAdapter{IDValue: "other", BasePathValue: "/other", Rename: func(ctx context.Context, op Operation, next Next) (Result, error) {
		return Result{}, nil
	}}
	router, err := NewRouter([]Mount{root, other})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := router.Dispatch(ctx, Operation{Kind: OpRename, OldPath: "/a", NewPath: "/b"}); err != nil {
		t.Fatal(err)
	}
	if gotRename.OldPath != "/a" || gotRename.NewPath != "/b" {
		t.Fatalf("rename op = %#v", gotRename)
	}
	_, err = router.Dispatch(ctx, Operation{Kind: OpRename, OldPath: "/a", NewPath: "/other/a"})
	if ErrnoOf(err) != syscall.EXDEV {
		t.Fatalf("cross-mount rename err = %v, want EXDEV", err)
	}
}

func TestRouterPropagatesMountEXDEV(t *testing.T) {
	root := MountAdapter{IDValue: "root", BasePathValue: "/", Rename: func(ctx context.Context, op Operation, next Next) (Result, error) {
		return Result{}, syscall.EXDEV
	}}
	router, err := NewRouter([]Mount{root})
	if err != nil {
		t.Fatal(err)
	}
	_, err = router.Dispatch(context.Background(), Operation{Kind: OpRename, OldPath: "/a", NewPath: "/b"})
	if ErrnoOf(err) != syscall.EXDEV {
		t.Fatalf("rename err = %v, want EXDEV", err)
	}
}

func TestSyntheticDirectoriesInReadDirAndGetAttr(t *testing.T) {
	ctx := context.Background()
	root, err := NewEmptyDirMount("root", "/", 0o777)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := NewEmptyDirMount("leaf", "/foo/bar/baz", 0o777)
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter([]Mount{root, leaf})
	if err != nil {
		t.Fatal(err)
	}
	res, err := router.Dispatch(ctx, Operation{Kind: OpReadDir, Path: "/"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 1 || res.Entries[0].Name != "foo" || res.Entries[0].Mode&syscall.S_IFMT != syscall.S_IFDIR {
		t.Fatalf("root entries = %#v", res.Entries)
	}
	first, err := router.Dispatch(ctx, Operation{Kind: OpGetAttr, Path: "/foo"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := router.Dispatch(ctx, Operation{Kind: OpGetAttr, Path: "/foo"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Attr.Object.Kind != "synthetic-dir" || first.Attr.Inode != second.Attr.Inode {
		t.Fatalf("synthetic attrs = %#v %#v", first.Attr, second.Attr)
	}
}

func TestRealDirectoryEntryWinsOverSyntheticEntry(t *testing.T) {
	ctx := context.Background()
	source := t.TempDir()
	if err := os.Mkdir(filepath.Join(source, "foo"), 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := NewPassthroughMount(PassthroughOptions{ID: "root", BasePath: "/", Source: source})
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := NewEmptyDirMount("leaf", "/foo/bar/baz", 0o777)
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter([]Mount{root, leaf})
	if err != nil {
		t.Fatal(err)
	}
	res, err := router.Dispatch(ctx, Operation{Kind: OpReadDir, Path: "/"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 1 || res.Entries[0].Object.Kind != "passthrough" {
		t.Fatalf("entries = %#v", res.Entries)
	}
}

func TestSyntheticParentMaterializedBeforeCreate(t *testing.T) {
	ctx := context.Background()
	source := t.TempDir()
	root, err := NewPassthroughMount(PassthroughOptions{ID: "root", BasePath: "/", Source: source})
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := NewEmptyDirMount("leaf", "/foo/bar/baz", 0o777)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &RecordingInvalidator{}
	router, err := NewRouterWithOptions([]Mount{root, leaf}, RouterOptions{Invalidator: recorder})
	if err != nil {
		t.Fatal(err)
	}
	res, err := router.Dispatch(ctx, Operation{Kind: OpCreate, Path: "/foo/bar/hello", Flags: syscall.O_RDWR, Mode: 0o644})
	if err != nil {
		t.Fatal(err)
	}
	if res.Handle == nil {
		t.Fatal("expected file handle")
	}
	if err := res.Handle.(FileReleaser).Release(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(source, "foo", "bar", "hello")); err != nil {
		t.Fatal(err)
	}
	parent, err := router.Dispatch(ctx, Operation{Kind: OpGetAttr, Path: "/foo/bar"})
	if err != nil {
		t.Fatal(err)
	}
	if parent.Attr.Object.Kind != "passthrough" {
		t.Fatalf("materialized parent kind = %q, want passthrough", parent.Attr.Object.Kind)
	}
	if len(recorder.EntryEvents()) == 0 {
		t.Fatal("expected invalidation events")
	}
}

func TestRouterReplacePublishesNewSnapshotAndInvalidates(t *testing.T) {
	recorder := &RecordingInvalidator{}
	first := testMount("first", "/", func(ctx context.Context, op Operation, next Next) (Result, error) {
		attr := testAttr("first")
		return Result{Attr: &attr}, nil
	})
	second := testMount("second", "/", func(ctx context.Context, op Operation, next Next) (Result, error) {
		attr := testAttr("second")
		return Result{Attr: &attr}, nil
	})
	router, err := NewRouterWithOptions([]Mount{first}, RouterOptions{Invalidator: recorder})
	if err != nil {
		t.Fatal(err)
	}
	if got := router.Snapshot().Mounts(); len(got) != 1 || got[0].ID() != "first" {
		t.Fatalf("snapshot = %#v", got)
	}
	if err := router.Replace([]Mount{second}); err != nil {
		t.Fatal(err)
	}
	res, err := router.Dispatch(context.Background(), Operation{Kind: OpGetAttr, Path: "/anything"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Attr.Object.MountID != "second" {
		t.Fatalf("route after replace = %q", res.Attr.Object.MountID)
	}
	if len(recorder.EntryEvents()) != 0 {
		t.Fatalf("root-only replace should not emit entry events: %#v", recorder.EntryEvents())
	}
}

func testMount(id, base string, handler OperationHandler) Mount {
	return MountAdapter{IDValue: id, BasePathValue: base, Handler: handler}
}

func testAttr(id string) Attr {
	return Attr{Object: ObjectKey{MountID: id, Kind: "test", Key: id}, Mode: syscall.S_IFREG | 0o644, Nlink: 1}
}
