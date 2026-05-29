package staticmount

import (
	"context"
	"syscall"
	"testing"

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
