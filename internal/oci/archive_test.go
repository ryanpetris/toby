package oci

// Exercises OCI archive extraction and path containment.

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"petris.dev/toby/internal/oci/imagesource"
)

func TestExtractOCIArchiveExtractsRegularFiles(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "image.tar")
	writeArchive(t, archivePath, []archiveTestEntry{
		{name: "oci-layout", data: "layout"},
		{name: "blobs/sha256/value", data: "blob"},
	})

	layoutPath := filepath.Join(t.TempDir(), "layout")
	if err := extractOCIArchive(
		t.Context(),
		archivePath,
		layoutPath,
	); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(
		filepath.Join(layoutPath, "blobs", "sha256", "value"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "blob" {
		t.Fatalf("extracted data = %q, want %q", data, "blob")
	}
}

func TestExtractOCIArchiveRejectsEscapingEntries(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "image.tar")
	writeArchive(t, archivePath, []archiveTestEntry{
		{name: "../outside", data: "content"},
	})

	parent := t.TempDir()
	if err := extractOCIArchive(
		t.Context(),
		archivePath,
		filepath.Join(parent, "layout"),
	); err == nil {
		t.Fatal("escaping archive entry succeeded")
	}
	if _, err := os.Stat(filepath.Join(parent, "outside")); !os.IsNotExist(err) {
		t.Fatalf("outside file stat error = %v", err)
	}
}

func TestExtractOCIArchiveRejectsSymbolicLinks(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "image.tar")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(file)
	if err := writer.WriteHeader(&tar.Header{
		Name:     "link",
		Typeflag: tar.TypeSymlink,
		Linkname: "/tmp/target",
	}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	if err := extractOCIArchive(
		t.Context(),
		archivePath,
		filepath.Join(t.TempDir(), "layout"),
	); err == nil {
		t.Fatal("symbolic-link archive entry succeeded")
	}
}

func TestStorePreparesOCIArchive(t *testing.T) {
	layoutPath := filepath.Join(t.TempDir(), "layout")
	if err := writeTestLayout(layoutPath, testPlatform()); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(t.TempDir(), "image.tar")
	writeDirectoryArchive(t, archivePath, layoutPath)

	pipeline := &fakePipeline{}
	store := newTestStore(t, pipeline)
	prepared, err := store.Prepare(t.Context(), Request{
		Source:    imagesource.Archive,
		Reference: "example.local/imported:latest",
		Archive:   archivePath,
		Platform:  testPlatform(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Close(); err != nil {
		t.Fatal(err)
	}

	pulls, extractions := pipeline.counts()
	if pulls != 0 || extractions != 1 {
		t.Fatalf(
			"pipeline calls = (%d, %d), want (0, 1)",
			pulls,
			extractions,
		)
	}
}

type archiveTestEntry struct {
	name string
	data string
}

func writeArchive(
	t *testing.T,
	path string,
	entries []archiveTestEntry,
) {
	t.Helper()

	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(file)
	for _, entry := range entries {
		data := []byte(entry.data)
		if err := writer.WriteHeader(&tar.Header{
			Name:     entry.name,
			Mode:     0o644,
			Size:     int64(len(data)),
			Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := bytes.NewReader(data).WriteTo(writer); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeDirectoryArchive(
	t *testing.T,
	archivePath string,
	sourcePath string,
) {
	t.Helper()

	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(file)
	err = filepath.WalkDir(
		sourcePath,
		func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if path == sourcePath {
				return nil
			}
			relative, err := filepath.Rel(sourcePath, path)
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			header, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			header.Name = filepath.ToSlash(relative)
			if err := writer.WriteHeader(header); err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			source, err := os.Open(path)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(writer, source)
			closeErr := source.Close()
			if copyErr != nil {
				return copyErr
			}
			return closeErr
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
