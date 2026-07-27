// Package protocol defines the sandbox gateway application-protocol version.
package protocol

import (
	"errors"
	"fmt"
)

const (
	// Version is the sandbox application-protocol version understood by this
	// binary.
	Version uint32 = 1
)

// ErrVersionMismatch identifies an unsupported sandbox protocol version.
var ErrVersionMismatch = errors.New("sandbox protocol version mismatch")

// VersionError reports the received and locally supported protocol versions.
type VersionError struct {
	Received  uint32
	Supported []uint32
}

// Error describes the incompatible versions.
func (e VersionError) Error() string {
	return fmt.Sprintf(
		"%s: endpoint advertised %d, client supports %v",
		ErrVersionMismatch,
		e.Received,
		e.Supported,
	)
}

// Unwrap supports errors.Is(err, ErrVersionMismatch).
func (e VersionError) Unwrap() error {
	return ErrVersionMismatch
}

// SupportsVersion reports whether this client can select the advertised
// sandbox protocol version.
func SupportsVersion(version uint32) bool {
	for _, supported := range SupportedVersions() {
		if version == supported {
			return true
		}
	}

	return false
}

// SupportedVersions returns the sandbox protocol versions implemented by this
// binary.
func SupportedVersions() []uint32 {
	return []uint32{Version}
}
