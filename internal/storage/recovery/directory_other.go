//go:build !linux

package recovery

// Reports unsupported-platform errors for directory publication recovery.

import (
	"fmt"

	"petris.dev/toby/internal/storage/safefs"
)

// CleanupTemporaryDirectories reports that secure temporary recovery requires
// Linux.
func CleanupTemporaryDirectories(*safefs.Directory, uint64, uint64) error {
	return fmt.Errorf("%w: temporary directory recovery requires Linux", safefs.ErrUnsupported)
}
