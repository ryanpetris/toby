package protocol

// Defines errors callers can classify during agent-protocol validation.

import "errors"

var (
	// ErrConfigurationTooLarge means one bounded configuration document cannot
	// fit the agent's internal safety contract.
	ErrConfigurationTooLarge = errors.New(
		"agent configuration document is too large",
	)

	// ErrVersionMismatch means the agent advertises an unsupported application
	// protocol version.
	ErrVersionMismatch = errors.New("agent protocol version mismatch")
)
