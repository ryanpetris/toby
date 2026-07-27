package sandboxgateway

// Defines stable endpoint and descriptor errors.

import (
	"errors"
	"fmt"
)

var (
	// ErrPathInUse reports that the requested socket path is already occupied.
	ErrPathInUse = errors.New("sandbox socket path is already in use")
)

// DescriptorError reports an invalid capability without revealing its host
// socket path.
type DescriptorError struct {
	Message string
	Cause   error
}

// Error returns the bounded structural diagnostic.
func (e *DescriptorError) Error() string {
	if e == nil || e.Message == "" {
		return "invalid sandbox gateway descriptor"
	}

	return fmt.Sprintf(
		"invalid sandbox gateway descriptor: %s",
		e.Message,
	)
}

// Unwrap exposes the structural failure to trusted callers.
func (e *DescriptorError) Unwrap() error {
	if e == nil {
		return nil
	}

	return e.Cause
}
