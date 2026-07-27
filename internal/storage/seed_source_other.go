//go:build !linux

package storage

// Reports that immutable seed-tree descriptor traversal requires Linux.

import (
	"fmt"
	"os"
)

func openImmutableSeedDirectory(
	*os.File,
	string,
	string,
) (*os.File, bool, error) {
	return nil, false, fmt.Errorf("secure immutable seed traversal requires Linux")
}
