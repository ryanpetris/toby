package fusekit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestFUSEPassthroughReadWrite(t *testing.T) {
	mnt, source := mountIntegration(t, func(t *testing.T) []Mount {
		source := t.TempDir()
		if err := os.WriteFile(filepath.Join(source, "file"), []byte("hello"), 0o644); err != nil {
			t.Fatal(err)
		}
		mount, err := NewPassthroughMount(PassthroughOptions{ID: "root", BasePath: "/", Source: source})
		if err != nil {
			t.Fatal(err)
		}
		return []Mount{mount}
	})
	data, err := os.ReadFile(filepath.Join(mnt, "file"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("read = %q, want hello", data)
	}
	if err := os.WriteFile(filepath.Join(mnt, "created"), []byte("world"), 0o644); err != nil {
		t.Fatal(err)
	}
	created, err := os.ReadFile(filepath.Join(source, "created"))
	if err != nil {
		t.Fatal(err)
	}
	if string(created) != "world" {
		t.Fatalf("created = %q, want world", created)
	}
}

func TestFUSEReadOnlyPassthroughWriteFailure(t *testing.T) {
	mnt, _ := mountIntegration(t, func(t *testing.T) []Mount {
		rootSource := t.TempDir()
		roSource := t.TempDir()
		root, err := NewPassthroughMount(PassthroughOptions{ID: "root", BasePath: "/", Source: rootSource})
		if err != nil {
			t.Fatal(err)
		}
		readonly, err := NewPassthroughMount(PassthroughOptions{ID: "ro", BasePath: "/ro", Source: roSource, ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		return []Mount{root, readonly}
	})
	err := os.WriteFile(filepath.Join(mnt, "ro", "file"), []byte("no"), 0o644)
	if err == nil {
		t.Fatal("expected read-only write failure")
	}
}

func TestFUSESyntheticParentMaterialization(t *testing.T) {
	mnt, source := mountIntegration(t, func(t *testing.T) []Mount {
		source := t.TempDir()
		root, err := NewPassthroughMount(PassthroughOptions{ID: "root", BasePath: "/", Source: source})
		if err != nil {
			t.Fatal(err)
		}
		leaf, err := NewEmptyDirMount("leaf", "/foo/bar/baz", 0o777)
		if err != nil {
			t.Fatal(err)
		}
		return []Mount{root, leaf}
	})
	if info, err := os.Stat(filepath.Join(mnt, "foo")); err != nil || !info.IsDir() {
		t.Fatalf("synthetic foo info = %#v err = %v", info, err)
	}
	if err := os.WriteFile(filepath.Join(mnt, "foo", "bar", "hello"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(source, "foo", "bar", "hello")); err != nil {
		t.Fatal(err)
	}
}

func TestFUSECrossMountRenameReturnsEXDEV(t *testing.T) {
	mnt, _ := mountIntegration(t, func(t *testing.T) []Mount {
		rootSource := t.TempDir()
		otherSource := t.TempDir()
		if err := os.WriteFile(filepath.Join(rootSource, "file"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		root, err := NewPassthroughMount(PassthroughOptions{ID: "root", BasePath: "/", Source: rootSource})
		if err != nil {
			t.Fatal(err)
		}
		other, err := NewPassthroughMount(PassthroughOptions{ID: "other", BasePath: "/other", Source: otherSource})
		if err != nil {
			t.Fatal(err)
		}
		return []Mount{root, other}
	})
	err := os.Rename(filepath.Join(mnt, "file"), filepath.Join(mnt, "other", "file"))
	if !errors.Is(err, syscall.EXDEV) {
		t.Fatalf("rename err = %v, want EXDEV", err)
	}
}

func mountIntegration(t *testing.T, build func(*testing.T) []Mount) (string, string) {
	t.Helper()
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skip("/dev/fuse is unavailable")
	}
	mounts := build(t)
	var source string
	if passthrough, ok := mounts[0].(*PassthroughMount); ok {
		source = passthrough.Source()
	}
	router, err := NewRouter(mounts)
	if err != nil {
		t.Fatal(err)
	}
	mnt := filepath.Join(t.TempDir(), "mnt")
	if err := os.Mkdir(mnt, 0o755); err != nil {
		t.Fatal(err)
	}
	server, err := MountServer(context.Background(), mnt, router)
	if err != nil {
		t.Skipf("FUSE mount not permitted: %v", err)
	}
	t.Cleanup(func() { _ = server.Unmount() })
	return mnt, source
}
