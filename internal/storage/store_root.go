package storage

// Exposes a caller-owned duplicate of the authoritative persistent-data root
// for host-storage boundary validation.

import (
	"fmt"
	"os"
)

// DataRootFile returns a caller-owned descriptor for all Toby persistent data,
// including every volume and image.
func (s *Store) DataRootFile() (*os.File, error) {
	if s == nil || s.dataRoot == nil {
		return nil, fmt.Errorf("volume store is closed")
	}

	return s.dataRoot.File()
}
