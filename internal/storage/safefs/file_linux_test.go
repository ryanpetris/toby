//go:build linux

package safefs

// Tests bounded regular-file I/O, containment, and atomic replacement.

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRegularFileReadWriteAndBounds(t *testing.T) {
	directory, path := testDirectory(t)

	if err := directory.WriteFile("state", []byte("first"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := directory.WriteFile("state", []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}

	data, err := directory.ReadFile("state", int64(len("second")))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "second" {
		t.Fatalf("data = %q", data)
	}

	info, err := os.Stat(filepath.Join(path, "state"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %04o, want 0600", info.Mode().Perm())
	}

	if _, err := directory.ReadFile("state", int64(len("second")-1)); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("bounded read error = %v, want ErrLimitExceeded", err)
	}
}

func TestOpenFileRetainsValidatedRegularInode(t *testing.T) {
	directory, path := testDirectory(t)
	originalPath := filepath.Join(path, "blob")
	if err := os.WriteFile(originalPath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}

	file, err := directory.OpenFile("blob")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	if err := os.Rename(originalPath, filepath.Join(path, "moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(originalPath, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}

	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original" {
		t.Fatalf("opened inode data = %q, want original", data)
	}
}

func TestRegularFileOperationsRejectTraversalSymlinksAndTypes(t *testing.T) {
	directory, path := testDirectory(t)
	outside := filepath.Join(filepath.Dir(path), filepath.Base(path)+"-outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		os.RemoveAll(outside)
	})
	outsideFile := filepath.Join(outside, "secret")
	if err := os.WriteFile(outsideFile, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"", ".", "../secret", "/absolute", "a/../secret", "a//secret"} {
		if err := directory.WriteFile(name, []byte("bad"), 0o600); !errors.Is(err, ErrUnsafePath) {
			t.Errorf("WriteFile(%q) error = %v, want ErrUnsafePath", name, err)
		}
	}

	if err := os.Symlink(outsideFile, filepath.Join(path, "file-link")); err != nil {
		t.Fatal(err)
	}
	if _, err := directory.ReadFile("file-link", 1024); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("symlink read error = %v, want ErrUnsafePath", err)
	}
	if err := directory.WriteFile("file-link", []byte("bad"), 0o600); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("symlink write error = %v, want ErrUnsafePath", err)
	}

	if err := os.Symlink(outside, filepath.Join(path, "parent-link")); err != nil {
		t.Fatal(err)
	}
	if _, err := directory.ReadFile("parent-link/secret", 1024); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("intermediate symlink error = %v, want ErrUnsafePath", err)
	}

	if err := os.Mkdir(filepath.Join(path, "directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := directory.ReadFile("directory", 1024); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("directory read error = %v, want ErrUnsafePath", err)
	}

	if err := unix.Mkfifo(filepath.Join(path, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := directory.ReadFile("fifo", 1024); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("FIFO read error = %v, want ErrUnsafePath", err)
	}

	data, err := os.ReadFile(outsideFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "unchanged" {
		t.Fatalf("outside file changed to %q", data)
	}
}

func TestReplaceFileIsAtomicAndReplacesExistingEntries(t *testing.T) {
	directory, path := testDirectory(t)

	if err := directory.WriteFile("mapping", []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := directory.ReplaceFile("mapping", []byte("new-complete-value"), 0o640); err != nil {
		t.Fatal(err)
	}
	data, err := directory.ReadFile("mapping", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new-complete-value" {
		t.Fatalf("replacement data = %q", data)
	}
	info, err := os.Stat(filepath.Join(path, "mapping"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("replacement mode = %04o, want 0640", info.Mode().Perm())
	}

	if err := directory.ReplaceFile("new-mapping", []byte("created"), 0o600); err != nil {
		t.Fatal(err)
	}

	outside := filepath.Join(filepath.Dir(path), filepath.Base(path)+"-replace-outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		os.Remove(outside)
	})
	link := filepath.Join(path, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if err := directory.ReplaceFile("link", []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkData, err := os.ReadFile(link)
	if err != nil {
		t.Fatal(err)
	}
	if string(linkData) != "replacement" {
		t.Fatalf("replacement link data = %q", linkData)
	}

	fifo := filepath.Join(path, "fifo-replacement")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := directory.ReplaceFile("fifo-replacement", []byte("fifo replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	fifoData, err := os.ReadFile(fifo)
	if err != nil {
		t.Fatal(err)
	}
	if string(fifoData) != "fifo replacement" {
		t.Fatalf("replacement FIFO data = %q", fifoData)
	}

	if err := os.Mkdir(filepath.Join(path, "directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := directory.ReplaceFile("directory", []byte("bad"), 0o600); err == nil {
		t.Fatal("replacing a directory with a file succeeded")
	}

	outsideData, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(outsideData) != "outside" {
		t.Fatalf("outside replacement target changed to %q", outsideData)
	}
	assertNoTemporaryNames(t, path)
}
