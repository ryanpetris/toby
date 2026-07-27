//go:build linux

package safefs

// Verifies that directory construction retains exact descriptor authority
// without reopening its diagnostic path.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestOpenDirectoryFileUsesExactDescriptorNotDiagnosticPath(t *testing.T) {
	root, path := testDirectory(t)
	original, err := root.MkdirAll("original/nested")
	if err != nil {
		t.Fatal(err)
	}
	if err := original.WriteFile("marker", []byte("retained"), 0o600); err != nil {
		original.Close()
		t.Fatal(err)
	}
	if err := original.Close(); err != nil {
		t.Fatal(err)
	}

	source, err := os.Open(filepath.Join(path, "original"))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()

	const diagnosticPath = "/diagnostic/path/that/is/not/opened"
	retained, err := OpenDirectoryFile(source, diagnosticPath, testDirectoryOptions(os.Getuid()))
	if err != nil {
		t.Fatal(err)
	}
	defer retained.Close()
	if retained.Path() != diagnosticPath {
		t.Fatalf("Path = %q, want diagnostic label", retained.Path())
	}

	if err := os.Rename(
		filepath.Join(path, "original"),
		filepath.Join(path, "moved"),
	); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(path, "original"), 0o700); err != nil {
		t.Fatal(err)
	}

	nested, err := retained.OpenDirectory("nested")
	if err != nil {
		t.Fatal(err)
	}
	defer nested.Close()
	data, err := nested.ReadFile("marker", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "retained" {
		t.Fatalf("retained marker = %q", data)
	}
}

func TestOpenDirectoryFileRejectsUnsafeDescriptor(t *testing.T) {
	_, path := testDirectory(t)

	regularPath := filepath.Join(path, "regular")
	if err := os.WriteFile(regularPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	regular, err := os.Open(regularPath)
	if err != nil {
		t.Fatal(err)
	}
	defer regular.Close()
	if _, err := OpenDirectoryFile(regular, "regular", testDirectoryOptions(os.Getuid())); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("regular file error = %v, want ErrUnsafePath", err)
	}

	unsafePath := filepath.Join(path, "unsafe")
	if err := os.Mkdir(unsafePath, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unsafePath, 0o777); err != nil {
		t.Fatal(err)
	}
	unsafe, err := os.Open(unsafePath)
	if err != nil {
		t.Fatal(err)
	}
	defer unsafe.Close()
	opened, err := OpenDirectoryFile(unsafe, "unsafe", testDirectoryOptions(os.Getuid()))
	if err != nil {
		t.Fatalf("open writable directory: %v", err)
	}
	if err := opened.Close(); err != nil {
		t.Fatal(err)
	}

	pathFD, err := os.OpenFile(unsafePath, unix.O_PATH|unix.O_DIRECTORY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer pathFD.Close()
	if _, err := OpenDirectoryFile(pathFD, "path-only", testDirectoryOptions(os.Getuid())); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("O_PATH directory error = %v, want ErrUnsafePath", err)
	}
}
