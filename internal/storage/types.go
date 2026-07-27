package storage

// Declares the immutable rootfs seed-source contract.

import "os"

// SeedSource identifies an optional directory in an immutable rootfs snapshot.
type SeedSource struct {
	// Root is a caller-owned ordinary read-only descriptor for the exact leased
	// rootfs snapshot. Storage duplicates it before traversal and never reopens
	// a path to find the root.
	Root *os.File

	// RootDescription is optional non-authoritative text used in diagnostics.
	RootDescription string

	ImagePath string
}
