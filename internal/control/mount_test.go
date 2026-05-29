package control

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"petris.dev/toby/fusekit"
)

func TestControlMountWriteInvokesHandler(t *testing.T) {
	ctx := context.Background()
	request, err := NewProjectMountRequest(1, "foo")
	if err != nil {
		t.Fatal(err)
	}
	var got []byte
	router, err := fusekit.NewRouter([]fusekit.Mount{NewMount(func(ctx context.Context, data []byte) ([]byte, error) {
		got = append([]byte(nil), data...)
		return ResponseOK(nil, MountResult{HostPath: "/home/petris/Projects/foo", SandboxPath: "/home/petris/Projects/foo", VirtualPath: "/Projects/foo"}), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	res, err := router.Dispatch(ctx, fusekit.Operation{Kind: fusekit.OpOpen, Path: ControlPath, Flags: syscall.O_RDWR})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := res.Handle.(fusekit.FileWriter).Write(ctx, append(request, '\n'), 0); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, request) {
		t.Fatalf("request = %s, want %s", got, request)
	}
	response, err := res.Handle.(fusekit.FileReader).Read(ctx, make([]byte, 4096), 0)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeResponse(bytes.TrimSpace(response))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Error != nil {
		t.Fatalf("response error = %#v", decoded.Error)
	}
}

func TestControlMountWriteReturnsHandlerErrno(t *testing.T) {
	ctx := context.Background()
	request, err := NewProjectMountRequest(1, "foo")
	if err != nil {
		t.Fatal(err)
	}
	router, err := fusekit.NewRouter([]fusekit.Mount{NewMount(func(context.Context, []byte) ([]byte, error) {
		return ResponseError(nil, CodeDenied, "denied", nil), syscall.EACCES
	})})
	if err != nil {
		t.Fatal(err)
	}
	res, err := router.Dispatch(ctx, fusekit.Operation{Kind: fusekit.OpOpen, Path: ControlPath, Flags: syscall.O_RDWR})
	if err != nil {
		t.Fatal(err)
	}
	_, err = res.Handle.(fusekit.FileWriter).Write(ctx, append(request, '\n'), 0)
	if fusekit.ErrnoOf(err) != syscall.EACCES {
		t.Fatalf("err = %v, want EACCES", err)
	}
	response, err := res.Handle.(fusekit.FileReader).Read(ctx, make([]byte, 4096), 0)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeResponse(bytes.TrimSpace(response))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Error == nil || decoded.Error.Code != CodeDenied {
		t.Fatalf("response = %#v, want denied error", decoded)
	}
}

func TestControlMountReadOpenDenied(t *testing.T) {
	router, err := fusekit.NewRouter([]fusekit.Mount{NewMount(nil)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = router.Dispatch(context.Background(), fusekit.Operation{Kind: fusekit.OpOpen, Path: ControlPath, Flags: syscall.O_RDONLY})
	if fusekit.ErrnoOf(err) != syscall.EACCES {
		t.Fatalf("err = %v, want EACCES", err)
	}
}

func TestControlMountMergesBinaryDirectory(t *testing.T) {
	tobyMount, err := NewMountWithCurrentBinary(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tobyMount.Close() })
	router, err := fusekit.NewRouter([]fusekit.Mount{tobyMount})
	if err != nil {
		t.Fatal(err)
	}
	res, err := router.Dispatch(context.Background(), fusekit.Operation{Kind: fusekit.OpGetAttr, Path: BasePath})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Attr.Mode & 0o777; got != 0o500 {
		t.Fatalf("%s mode = %#o, want 0500", BasePath, got)
	}
	res, err = router.Dispatch(context.Background(), fusekit.Operation{Kind: fusekit.OpGetAttr, Path: BinPath})
	if err != nil {
		t.Fatal(err)
	}
	if res.Attr == nil || !res.Attr.IsDir() {
		t.Fatalf("%s attr = %#v, want dir", BinPath, res.Attr)
	}
	if got := res.Attr.Mode & 0o777; got != 0o500 {
		t.Fatalf("%s mode = %#o, want 0500", BinPath, got)
	}
	res, err = router.Dispatch(context.Background(), fusekit.Operation{Kind: fusekit.OpReadDir, Path: BasePath})
	if err != nil {
		t.Fatal(err)
	}
	if !hasEntry(res.Entries, "control") || !hasEntry(res.Entries, "bin") || !hasEntry(res.Entries, "static") {
		t.Fatalf("entries = %#v, want control, bin, and static", res.Entries)
	}
	res, err = router.Dispatch(context.Background(), fusekit.Operation{Kind: fusekit.OpGetAttr, Path: StaticPath})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Attr.Mode & 0o777; got != 0o500 {
		t.Fatalf("%s mode = %#o, want 0500", StaticPath, got)
	}
	res, err = router.Dispatch(context.Background(), fusekit.Operation{Kind: fusekit.OpReadDir, Path: BinPath})
	if err != nil {
		t.Fatal(err)
	}
	if !hasEntry(res.Entries, "toby") {
		t.Fatalf("entries = %#v, want toby", res.Entries)
	}
	res, err = router.Dispatch(context.Background(), fusekit.Operation{Kind: fusekit.OpOpen, Path: BinaryPath, Flags: syscall.O_RDONLY})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Attr.Mode & 0o777; got != 0o500 {
		t.Fatalf("%s mode = %#o, want 0500", BinaryPath, got)
	}
}

func TestControlMountBinaryUsesOpenFileReference(t *testing.T) {
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
	mount, err := newMountWithBinaryFDAt(BasePath, nil, fd)
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
	res, err := router.Dispatch(ctx, fusekit.Operation{Kind: fusekit.OpOpen, Path: BinaryPath, Flags: syscall.O_RDONLY})
	if err != nil {
		t.Fatal(err)
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

func TestControlMountAtCustomBasePath(t *testing.T) {
	mount, err := NewMountAt("/.state/toby", nil)
	if err != nil {
		t.Fatal(err)
	}
	router, err := fusekit.NewRouter([]fusekit.Mount{mount})
	if err != nil {
		t.Fatal(err)
	}
	res, err := router.Dispatch(context.Background(), fusekit.Operation{Kind: fusekit.OpReadDir, Path: "/.state/toby"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasEntry(res.Entries, "control") || !hasEntry(res.Entries, "bin") || !hasEntry(res.Entries, "static") {
		t.Fatalf("entries = %#v, want control, bin, and static", res.Entries)
	}
	if _, err := router.Dispatch(context.Background(), fusekit.Operation{Kind: fusekit.OpGetAttr, Path: "/.state/toby/control"}); err != nil {
		t.Fatal(err)
	}
}

func TestControlMountDoesNotPassThroughUnknownPaths(t *testing.T) {
	ctx := context.Background()
	source := t.TempDir()
	lowerToby := filepath.Join(source, ".local", "state", "toby")
	if err := os.MkdirAll(lowerToby, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lowerToby, "secret"), []byte("host"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, err := fusekit.NewPassthroughMount(fusekit.PassthroughOptions{ID: "home-root", BasePath: "/", Source: source})
	if err != nil {
		t.Fatal(err)
	}
	router, err := fusekit.NewRouter([]fusekit.Mount{root, NewMount(nil)})
	if err != nil {
		t.Fatal(err)
	}

	res, err := router.Dispatch(ctx, fusekit.Operation{Kind: fusekit.OpReadDir, Path: BasePath})
	if err != nil {
		t.Fatal(err)
	}
	if hasEntry(res.Entries, "secret") {
		t.Fatalf("entries = %#v, want lower mount entry hidden", res.Entries)
	}
	_, err = router.Dispatch(ctx, fusekit.Operation{Kind: fusekit.OpGetAttr, Path: BasePath + "/secret"})
	if fusekit.ErrnoOf(err) != syscall.ENOENT {
		t.Fatalf("getattr err = %v, want ENOENT", err)
	}
	_, err = router.Dispatch(ctx, fusekit.Operation{Kind: fusekit.OpOpen, Path: BasePath + "/new", Flags: syscall.O_WRONLY | syscall.O_CREAT})
	if fusekit.ErrnoOf(err) != syscall.EROFS {
		t.Fatalf("open create err = %v, want EROFS", err)
	}
	_, err = router.Dispatch(ctx, fusekit.Operation{Kind: fusekit.OpCreate, Path: ControlPath, Flags: syscall.O_RDWR, Mode: 0o600})
	if fusekit.ErrnoOf(err) != syscall.EROFS {
		t.Fatalf("create control err = %v, want EROFS", err)
	}
	_, err = router.Dispatch(ctx, fusekit.Operation{Kind: fusekit.OpSetAttr, Path: ControlPath})
	if fusekit.ErrnoOf(err) != syscall.EROFS {
		t.Fatalf("setattr control err = %v, want EROFS", err)
	}
}

func TestControlMountFUSERoundTrip(t *testing.T) {
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skip("/dev/fuse is unavailable")
	}
	root, err := fusekit.NewEmptyDirMount("root", "/", 0o777)
	if err != nil {
		t.Fatal(err)
	}
	controlMount := NewMount(func(ctx context.Context, data []byte) ([]byte, error) {
		request, err := DecodeRequest(data)
		if err != nil {
			return ResponseError(nil, CodeInvalidRequest, err.Error(), nil), syscall.EINVAL
		}
		params, err := DecodeProjectParams(request.Params)
		if err != nil {
			return ResponseError(request.ID, CodeInvalidParams, err.Error(), nil), syscall.EINVAL
		}
		path := filepath.Join("/home/petris/Projects", params.Name)
		return ResponseOK(request.ID, MountResult{HostPath: path, SandboxPath: path, VirtualPath: "/mounted"}), nil
	})
	router, err := fusekit.NewRouter([]fusekit.Mount{root, controlMount})
	if err != nil {
		t.Fatal(err)
	}
	mountpoint := filepath.Join(t.TempDir(), "mnt")
	if err := os.Mkdir(mountpoint, 0o755); err != nil {
		t.Fatal(err)
	}
	server, err := fusekit.MountServer(context.Background(), mountpoint, router)
	if err != nil {
		t.Skipf("FUSE mount not permitted: %v", err)
	}
	t.Cleanup(func() { _ = server.Unmount() })

	result, err := NewClient(filepath.Join(mountpoint, filepath.FromSlash(ControlPath[1:]))).ProjectMount("foo")
	if err != nil {
		t.Fatal(err)
	}
	if result.HostPath != "/home/petris/Projects/foo" || result.VirtualPath != "/mounted" {
		t.Fatalf("result = %#v", result)
	}
}

func hasEntry(entries []fusekit.DirEntry, name string) bool {
	for _, entry := range entries {
		if entry.Name == name {
			return true
		}
	}
	return false
}
