package protocol

// Reports incompatible agent wire versions without exposing message contents.

import "fmt"

// VersionError reports the received and locally supported protocol versions.
type VersionError struct {
	Received  uint32
	Supported []uint32
}

// Error describes the incompatible versions.
func (e VersionError) Error() string {
	return fmt.Sprintf(
		"%s: agent advertised %d, client supports %v",
		ErrVersionMismatch,
		e.Received,
		e.Supported,
	)
}

// Unwrap supports errors.Is(err, ErrVersionMismatch).
func (e VersionError) Unwrap() error {
	return ErrVersionMismatch
}
