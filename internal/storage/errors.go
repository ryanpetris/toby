package storage

// Declares classifiable errors returned by persistent storage resolution.

import "errors"

var (
	errInvalidLimits        = errors.New("storage limits are outside supported bounds")
	errStorageObjectMissing = errors.New("persistent storage object is missing")
)

var (
	// ErrMetadataMismatch reports that a published object's metadata does not
	// match its requested identity or canonical schema.
	ErrMetadataMismatch = errors.New("persistent storage metadata mismatch")

	// ErrUnsupportedSeed reports an unsafe or unsupported seed entry.
	ErrUnsupportedSeed = errors.New("unsupported seed entry")

	// ErrSeedLimitExceeded reports that first-use seed traversal exceeded a
	// configured hard bound.
	ErrSeedLimitExceeded = errors.New("seed limit exceeded")

	// ErrConflictingRequest reports incompatible managed-directory requests
	// using one key.
	ErrConflictingRequest = errors.New("conflicting managed-directory request")

	// ErrVolumeBusy reports that a volume is retained by a running launch.
	ErrVolumeBusy = errors.New("persistent volume is in use")
)
