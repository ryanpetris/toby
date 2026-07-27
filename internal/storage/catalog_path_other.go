//go:build !linux

package storage

// Reports that exact retained-directory path resolution requires Linux.

import (
	"petris.dev/toby/internal/storage/safefs"
)

func resolvedDirectoryPath(*safefs.Directory) (string, error) {
	return "", safefs.ErrUnsupported
}
