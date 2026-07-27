package oci

// Extracts OCI image-layout tar archives into private temporary directories.

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"petris.dev/toby/internal/diagnostic"
)

func extractOCIArchive(
	ctx context.Context,
	archivePath string,
	layoutPath string,
) error {
	archive, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open OCI archive %q: %w", archivePath, err)
	}
	defer func() {
		diagnostic.DiscardError(
			"the archive is an input that has already been read",
			"close OCI archive",
			archive.Close(),
			"path",
			archivePath,
		)
	}()

	if err := os.Mkdir(layoutPath, 0o700); err != nil {
		return fmt.Errorf("create OCI layout directory: %w", err)
	}

	reader := tar.NewReader(archive)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read OCI archive: %w", err)
		}

		relative, err := cleanArchiveEntry(header.Name)
		if err != nil {
			return err
		}
		target := filepath.Join(
			layoutPath,
			filepath.FromSlash(relative),
		)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return fmt.Errorf(
					"create OCI archive directory %q: %w",
					header.Name,
					err,
				)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return fmt.Errorf(
					"create OCI archive parent for %q: %w",
					header.Name,
					err,
				)
			}
			if err := extractArchiveFile(reader, target, header); err != nil {
				return err
			}
		default:
			return fmt.Errorf(
				"OCI archive entry %q has unsupported type %d",
				header.Name,
				header.Typeflag,
			)
		}
	}
}

func cleanArchiveEntry(name string) (string, error) {
	if strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("OCI archive entry contains a NUL byte")
	}
	cleaned := path.Clean(name)
	if cleaned == "." ||
		path.IsAbs(cleaned) ||
		cleaned == ".." ||
		strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf(
			"OCI archive entry %q escapes the layout root",
			name,
		)
	}
	return cleaned, nil
}

func extractArchiveFile(
	reader io.Reader,
	target string,
	header *tar.Header,
) (returnErr error) {
	file, err := os.OpenFile(
		target,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		0o600,
	)
	if err != nil {
		return fmt.Errorf(
			"create OCI archive file %q: %w",
			header.Name,
			err,
		)
	}
	defer func() {
		if err := file.Close(); returnErr == nil && err != nil {
			returnErr = fmt.Errorf(
				"close OCI archive file %q: %w",
				header.Name,
				err,
			)
		}
	}()

	written, err := io.Copy(file, reader)
	if err != nil {
		return fmt.Errorf(
			"extract OCI archive file %q: %w",
			header.Name,
			err,
		)
	}
	if written != header.Size {
		return fmt.Errorf(
			"extract OCI archive file %q: wrote %d bytes, expected %d",
			header.Name,
			written,
			header.Size,
		)
	}
	return nil
}
