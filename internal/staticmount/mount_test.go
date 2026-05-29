package staticmount

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"testing/fstest"

	"petris.dev/toby/fusekit"
)

func TestStaticMountModesAreReadOnly(t *testing.T) {
	mount, err := New("static", "/toby/static", []File{{Path: "opencode/opencode.json", Data: []byte("{}")}})
	if err != nil {
		t.Fatal(err)
	}
	router, err := fusekit.NewRouter([]fusekit.Mount{mount})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/toby/static", "/toby/static/opencode"} {
		res, err := router.Dispatch(context.Background(), fusekit.Operation{Kind: fusekit.OpGetAttr, Path: path})
		if err != nil {
			t.Fatal(err)
		}
		if got := res.Attr.Mode & 0o777; got != 0o500 {
			t.Fatalf("%s mode = %#o, want 0500", path, got)
		}
	}
	res, err := router.Dispatch(context.Background(), fusekit.Operation{Kind: fusekit.OpGetAttr, Path: "/toby/static/opencode/opencode.json"})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Attr.Mode & 0o777; got != 0o400 {
		t.Fatalf("file mode = %#o, want 0400", got)
	}
	_, err = router.Dispatch(context.Background(), fusekit.Operation{Kind: fusekit.OpOpen, Path: "/toby/static/opencode/opencode.json", Flags: syscall.O_WRONLY})
	if fusekit.ErrnoOf(err) != syscall.EROFS {
		t.Fatalf("open write err = %v, want EROFS", err)
	}
}

func TestStaticMountReadsFromFSFileSource(t *testing.T) {
	ctx := context.Background()
	file, err := FromFS("bin/tool", fstest.MapFS{"embedded/tool": {Data: []byte("abcdef")}}, "embedded/tool", 0o500)
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Data) != 0 || file.Source == nil {
		t.Fatalf("file = %#v, want source-backed file", file)
	}
	mount, err := New("static", "/toby/static", []File{file})
	if err != nil {
		t.Fatal(err)
	}
	router, err := fusekit.NewRouter([]fusekit.Mount{mount})
	if err != nil {
		t.Fatal(err)
	}
	res, err := router.Dispatch(ctx, fusekit.Operation{Kind: fusekit.OpGetAttr, Path: "/toby/static/bin/tool"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Attr.Size != 6 || res.Attr.Mode&0o777 != 0o500 {
		t.Fatalf("attr = %#v, want size 6 mode 0500", res.Attr)
	}
	res, err = router.Dispatch(ctx, fusekit.Operation{Kind: fusekit.OpOpen, Path: "/toby/static/bin/tool", Flags: syscall.O_RDONLY})
	if err != nil {
		t.Fatal(err)
	}
	data, err := res.Handle.(fusekit.FileReader).Read(ctx, make([]byte, 3), 2)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "cde" {
		t.Fatalf("data = %q, want cde", data)
	}
}

func TestStaticMountHostFDSourceUsesOpenFileReference(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "toby")
	if err := os.WriteFile(path, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	mount, err := New("static", "/toby/static", []File{{Path: "bin/toby", Source: &hostFDSource{fd: fd}, Mode: 0o500}})
	if err != nil {
		_ = syscall.Close(fd)
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mount.Close() })
	if err := os.WriteFile(path+".new", []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".new", path); err != nil {
		t.Fatal(err)
	}
	replaced, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(replaced) != "new" {
		t.Fatalf("replaced binary = %q, want new", replaced)
	}
	router, err := fusekit.NewRouter([]fusekit.Mount{mount})
	if err != nil {
		t.Fatal(err)
	}
	res, err := router.Dispatch(ctx, fusekit.Operation{Kind: fusekit.OpOpen, Path: "/toby/static/bin/toby", Flags: syscall.O_RDONLY})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Attr.Mode & 0o777; got != 0o500 {
		t.Fatalf("mode = %#o, want 0500", got)
	}
	data, err := res.Handle.(fusekit.FileReader).Read(ctx, make([]byte, 16), 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old" {
		t.Fatalf("mounted binary = %q, want old", data)
	}
	if err := res.Handle.(fusekit.FileReleaser).Release(ctx); err != nil {
		t.Fatal(err)
	}
}
