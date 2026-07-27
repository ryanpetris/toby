package socket

// Defines errors shared by the private Unix socket implementations.

import "errors"

var (
	// ErrUnsafePath reports that the socket path or an existing filesystem
	// object violates the endpoint's path, type, or generation contract.
	ErrUnsafePath = errors.New("unsafe private socket path")

	// ErrUnsupported reports that private Unix sockets are unavailable on
	// the current platform.
	ErrUnsupported = errors.New("private Unix sockets are unsupported")
)
