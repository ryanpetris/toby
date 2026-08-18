//go:build linux

package safefs

// Tests bounded recursive deletion without symbolic-link traversal.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRemoveAllDoesNotFollowSymlinks(t *testing.T) {
	directory, path := testDirectory(t)
	outside := filepath.Join(filepath.Dir(path), filepath.Base(path)+"-remove-outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		os.RemoveAll(outside)
	})
	outsideFile := filepath.Join(outside, "keep")
	if err := os.WriteFile(outsideFile, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	tree := filepath.Join(path, "tree")
	if err := os.MkdirAll(filepath.Join(tree, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "nested", "file"), []byte("remove"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(tree, "outside-directory")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(tree, "outside-file")); err != nil {
		t.Fatal(err)
	}

	if err := directory.RemoveAll("tree", 100); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(tree); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("tree remains: %v", err)
	}
	data, err := os.ReadFile(outsideFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep" {
		t.Fatalf("outside data = %q", data)
	}

	if err := os.Symlink(outside, filepath.Join(path, "top-link")); err != nil {
		t.Fatal(err)
	}
	if err := directory.RemoveAll("top-link", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside directory was removed: %v", err)
	}
}

func TestRemoveAllHonorsBoundAndContainment(t *testing.T) {
	directory, path := testDirectory(t)

	tree := filepath.Join(path, "limited")
	if err := os.Mkdir(tree, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one", "two", "three"} {
		if err := os.WriteFile(filepath.Join(tree, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := directory.RemoveAllProgress("limited", 2)
	if !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("error = %v, want ErrLimitExceeded", err)
	}
	if removed != 1 {
		t.Fatalf("removed entries = %d, want 1", removed)
	}

	outside := filepath.Join(filepath.Dir(path), filepath.Base(path)+"-untouched")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		os.Remove(outside)
	})
	for _, name := range []string{"", ".", "../" + filepath.Base(outside), "/absolute", "a/../b"} {
		if err := directory.RemoveAll(name, 10); !errors.Is(err, ErrUnsafePath) {
			t.Errorf("RemoveAll(%q) error = %v, want ErrUnsafePath", name, err)
		}
	}

	data, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "outside" {
		t.Fatalf("outside file changed to %q", data)
	}
}

func TestRemoveAllMissingIsNoOp(t *testing.T) {
	directory, _ := testDirectory(t)
	if err := directory.RemoveAll("missing", 1); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveAllWidensRestrictiveDirectoriesThroughPinnedDescriptors(t *testing.T) {
	directory, path := testDirectory(t)

	tree := filepath.Join(path, "restrictive")
	nested := filepath.Join(tree, "nested")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "file"), []byte("remove"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		os.Chmod(tree, 0o700)
		os.Chmod(nested, 0o700)
		os.RemoveAll(tree)
	})

	if err := os.Chmod(nested, 0); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(tree, 0); err != nil {
		t.Fatal(err)
	}

	if err := directory.RemoveAll("restrictive", 3); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(tree); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restrictive tree remains: %v", err)
	}
}

func TestRemoveAllRemovesReadOnlyApplicationTrees(t *testing.T) {
	directory, path := testDirectory(t)

	// The Go module cache is the common case: read-only files inside
	// read-only directories, which no unlink can reach until the containing
	// directory regains write access.
	tree := filepath.Join(path, "module-cache")
	nested := filepath.Join(tree, "reference@v0.6.0")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, ".gitattributes"), []byte("cached"), 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(tree, 0o700)
		_ = os.Chmod(nested, 0o700)
		_ = os.RemoveAll(tree)
	})

	if err := os.Chmod(nested, 0o555); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(tree, 0o555); err != nil {
		t.Fatal(err)
	}

	if err := directory.RemoveAll("module-cache", 4); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(tree); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("module cache remains: %v", err)
	}
}

func TestValidateRemovalNameRejectsReplacement(t *testing.T) {
	directory, path := testDirectory(t)

	targetPath := filepath.Join(path, "target")
	movedPath := filepath.Join(path, "moved")
	if err := os.WriteFile(targetPath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}

	parentFD, name, diagnosticPath, err := directory.openParent("target")
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(parentFD)

	targetFD, err := openRelative(parentFD, name, unix.O_PATH, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(targetFD)

	state, err := newRemovalState(parentFD, path, 1)
	if err != nil {
		t.Fatal(err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(targetFD, &stat); err != nil {
		t.Fatal(err)
	}
	identity, err := validateRemovalDescriptor(targetFD, &stat, diagnosticPath, &state)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Rename(targetPath, movedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetPath, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := validateRemovalName(
		parentFD,
		name,
		identity,
		diagnosticPath,
		&state,
	); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("error = %v, want ErrUnsafePath", err)
	}

	data, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "replacement" {
		t.Fatalf("replacement data = %q", data)
	}
}

func TestRemoveEntryRejectsForeignDevice(t *testing.T) {
	directory, path := testDirectory(t)

	targetPath := filepath.Join(path, "foreign")
	if err := os.WriteFile(targetPath, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	parentFD, name, diagnosticPath, err := directory.openParent("foreign")
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(parentFD)

	state, err := newRemovalState(parentFD, path, 1)
	if err != nil {
		t.Fatal(err)
	}
	state.device++

	if err := removeEntry(parentFD, name, diagnosticPath, &state); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("error = %v, want ErrUnsafePath", err)
	}
	data, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep" {
		t.Fatalf("target data = %q", data)
	}
}

func TestRemoveEntryRejectsForeignMountOnSameDevice(t *testing.T) {
	directory, path := testDirectory(t)

	targetPath := filepath.Join(path, "foreign-mount")
	if err := os.WriteFile(targetPath, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	parentFD, name, diagnosticPath, err := directory.openParent("foreign-mount")
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(parentFD)

	state, err := newRemovalState(parentFD, path, 1)
	if err != nil {
		t.Fatal(err)
	}
	state.mountID++

	if err := removeEntry(parentFD, name, diagnosticPath, &state); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("error = %v, want ErrUnsafePath", err)
	}
	data, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep" {
		t.Fatalf("target data = %q", data)
	}
}

func TestRemoveAllRejectsSameDeviceBindMount(t *testing.T) {
	directory, path := testDirectory(t)

	source := filepath.Join(filepath.Dir(path), filepath.Base(path)+"-bind-source")
	mountpoint := filepath.Join(path, "tree", "mounted")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "keep"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(mountpoint, 0o700); err != nil {
		t.Fatal(err)
	}

	mounted := false
	t.Cleanup(func() {
		if mounted {
			if err := unix.Unmount(mountpoint, unix.MNT_DETACH); err != nil {
				t.Errorf("unmount bind test target: %v", err)
			}
		}
		os.RemoveAll(source)
	})

	if err := unix.Mount(source, mountpoint, "", unix.MS_BIND, ""); err != nil {
		if errors.Is(err, unix.EPERM) ||
			errors.Is(err, unix.EACCES) ||
			errors.Is(err, unix.ENOSYS) {
			t.Skipf("bind mounts unavailable: %v", err)
		}
		t.Fatal(err)
	}
	mounted = true

	var rootStat, mountedStat unix.Stat_t
	if err := unix.Stat(path, &rootStat); err != nil {
		t.Fatal(err)
	}
	if err := unix.Stat(mountpoint, &mountedStat); err != nil {
		t.Fatal(err)
	}
	if rootStat.Dev != mountedStat.Dev {
		t.Fatalf("bind mount device = %d, root device = %d", mountedStat.Dev, rootStat.Dev)
	}

	if err := directory.RemoveAll("tree/mounted/keep", 1); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("intermediate bind error = %v, want ErrUnsafePath", err)
	}
	if err := directory.RemoveAll("tree", 10); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("error = %v, want ErrUnsafePath", err)
	}
	data, err := os.ReadFile(filepath.Join(source, "keep"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep" {
		t.Fatalf("source data = %q", data)
	}
}
