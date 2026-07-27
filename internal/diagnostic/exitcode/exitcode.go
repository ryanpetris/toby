// Package exitcode maps errors to process exit codes. Error wraps an underlying
// error with an explicit Code (and is itself an error); FromError extracts the
// code an error should produce, defaulting to a generic failure.
package exitcode

import (
	"errors"
	"fmt"
)

// Error associates a process exit code with an optional cause.
type Error struct {
	Code int
	Err  error
}

var _ error = Error{}

// New constructs an exit error with a formatted cause.
func New(code int, format string, args ...any) Error {
	return Error{Code: code, Err: fmt.Errorf(format, args...)}
}

// Code constructs a silent exit error for code.
func Code(code int) Error {
	return Error{Code: code}
}

// Error returns the human-readable failure message.
func (e Error) Error() string {
	if e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

// Unwrap returns the underlying error.
func (e Error) Unwrap() error {
	return e.Err
}

// ExitCode returns the process exit code, defaulting zero to one.
func (e Error) ExitCode() int {
	if e.Code == 0 {
		return 1
	}
	return e.Code
}

// Silent reports whether the error intentionally has no message.
func (e Error) Silent() bool {
	return e.Err == nil
}

// FromError derives the process exit code represented by err.
func FromError(err error) int {
	if err == nil {
		return 0
	}
	var coded interface{ ExitCode() int }
	if errors.As(err, &coded) {
		return coded.ExitCode()
	}
	return 1
}

// IsSilent reports whether err intentionally has no user-facing message.
func IsSilent(err error) bool {
	var silent interface{ Silent() bool }
	return errors.As(err, &silent) && silent.Silent()
}
