package fusekit

import (
	"context"
	"syscall"
	"testing"
)

func TestEmptyDirMountReportsDirectoryAndNoChildren(t *testing.T) {
	mount, err := NewEmptyDirMount("empty", "/empty", 0o777)
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter([]Mount{mount})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	attr, err := router.Dispatch(ctx, Operation{Kind: OpGetAttr, Path: "/empty"})
	if err != nil {
		t.Fatal(err)
	}
	if attr.Attr.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		t.Fatalf("mode = %#o, want dir", attr.Attr.Mode)
	}
	if attr.Attr.Mode&0o777 != 0o777 {
		t.Fatalf("permissions = %#o, want 0777", attr.Attr.Mode&0o777)
	}
	entries, err := router.Dispatch(ctx, Operation{Kind: OpReadDir, Path: "/empty"})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries.Entries) != 0 {
		t.Fatalf("entries = %#v, want empty", entries.Entries)
	}
}

func TestEmptyDirMountUsesConfiguredMode(t *testing.T) {
	mount, err := NewEmptyDirMount("root", "/", 0o500)
	if err != nil {
		t.Fatal(err)
	}
	res, err := mount.Handle(context.Background(), Operation{Kind: OpGetAttr, Path: "/"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Attr.Mode != syscall.S_IFDIR|0o500 {
		t.Fatalf("mode = %#o, want %#o", res.Attr.Mode, syscall.S_IFDIR|0o500)
	}
}

func TestEmptyDirMountMutationReturnsEROFS(t *testing.T) {
	mount, err := NewEmptyDirMount("empty", "/", 0o777)
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter([]Mount{mount})
	if err != nil {
		t.Fatal(err)
	}
	_, err = router.Dispatch(context.Background(), Operation{Kind: OpCreate, Path: "/file", Flags: syscall.O_RDWR, Mode: 0o644})
	if ErrnoOf(err) != syscall.EROFS {
		t.Fatalf("create err = %v, want EROFS", err)
	}
}

func TestEmptyDirReceivesCoordinatorSyntheticEntries(t *testing.T) {
	root, err := NewEmptyDirMount("root", "/", 0o777)
	if err != nil {
		t.Fatal(err)
	}
	child, err := NewEmptyDirMount("child", "/generated/path", 0o777)
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter([]Mount{root, child})
	if err != nil {
		t.Fatal(err)
	}
	res, err := router.Dispatch(context.Background(), Operation{Kind: OpReadDir, Path: "/"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 1 || res.Entries[0].Name != "generated" {
		t.Fatalf("entries = %#v", res.Entries)
	}
}
