package mcpgateway

// Classifies first-use backend startup errors so callers retry only transient
// failures.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

// StartError is a backend startup failure with an explicit retry policy.
type StartError struct {
	Err       error
	Retryable bool
}

// Error returns the wrapped startup failure message.
func (e StartError) Error() string {
	if e.Err == nil {
		return "MCP backend startup failed"
	}
	return e.Err.Error()
}

// Unwrap returns the wrapped startup failure.
func (e StartError) Unwrap() error {
	return e.Err
}

// Permanent marks a startup failure as non-retryable.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return StartError{Err: err, Retryable: false}
}

// RetryableStart reports whether first-use backend startup should be retried.
// Authentication, not-found, and explicitly permanent initialize failures fail
// immediately. Other errors retry until the caller context expires.
func RetryableStart(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var start StartError
	if errors.As(err, &start) {
		return start.Retryable
	}
	if status, ok := httpStatus(err); ok {
		return transientHTTPStatus(status)
	}
	return true
}

func httpStatus(err error) (int, bool) {
	var statusErr HTTPStatusError
	if errors.As(err, &statusErr) {
		return statusErr.Status, true
	}
	return 0, false
}

func transientHTTPStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// HTTPStatusError is an HTTP status observed during backend startup.
type HTTPStatusError struct {
	Status  int
	Details string
}

// Error returns the HTTP startup failure message.
func (e HTTPStatusError) Error() string {
	if e.Details == "" {
		return fmt.Sprintf("HTTP %d", e.Status)
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.Details)
}
