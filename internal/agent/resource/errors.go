package resource

// Defines stable lifecycle errors without exposing resource specifications or
// sensitive process details.

import (
	"errors"
	"fmt"
	"time"
)

var (
	// ErrShuttingDown indicates that Registry has permanently stopped accepting
	// work.
	ErrShuttingDown = errors.New("resource registry is shutting down")

	// ErrResourceUnavailable indicates that no ready generation can currently
	// serve the request.
	ErrResourceUnavailable = errors.New("resource is temporarily unavailable")

	// ErrLeaseClosed indicates that an operation requires an active lease.
	ErrLeaseClosed = errors.New("resource lease is closed")

	// ErrResourceExited indicates that the lease or connector's generation
	// terminated unexpectedly.
	ErrResourceExited = errors.New("resource generation exited")
)

// RetryError reports when a failed resource may be acquired again.
type RetryError struct {
	RetryAt time.Time
}

// Error returns the human-readable failure message.
func (e *RetryError) Error() string {
	return fmt.Sprintf("%s; retry at %s", ErrResourceUnavailable, e.RetryAt.Format(time.RFC3339Nano))
}

// Unwrap returns the underlying error.
func (e *RetryError) Unwrap() error {
	return ErrResourceUnavailable
}
