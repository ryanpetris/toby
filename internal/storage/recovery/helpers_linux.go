//go:build linux

package recovery

// Defines shared publication-artifact names, identities, and close helpers.

import (
	"fmt"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/storage/safefs"
)

const (
	temporaryPrefix       = ".toby-tmp-"
	temporaryRandomLength = 32
	recoveryReadBatch     = 64
)

type fileIdentity struct {
	device uint64
	inode  uint64
}

func isTemporaryName(name string) bool {
	if len(name) != len(temporaryPrefix)+temporaryRandomLength ||
		name[:len(temporaryPrefix)] != temporaryPrefix {
		return false
	}
	for _, character := range name[len(temporaryPrefix):] {
		if (character >= '0' && character <= '9') ||
			(character >= 'a' && character <= 'f') {
			continue
		}
		return false
	}

	return true
}

func unsafeTemporary(path, operation string, err error) error {
	return fmt.Errorf(
		"%w: %s temporary recovery target %q: %w",
		safefs.ErrUnsafePath,
		operation,
		path,
		err,
	)
}

func closeFile(file *os.File, operation string) error {
	if err := file.Close(); err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}

	return nil
}

func closeDescriptor(fd int, path string) error {
	if err := unix.Close(fd); err != nil {
		return &fs.PathError{
			Op:   "close temporary recovery descriptor",
			Path: path,
			Err:  err,
		}
	}

	return nil
}
