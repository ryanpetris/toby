package exitcode

import (
	"errors"
	"fmt"
)

type Error struct {
	Code int
	Err  error
}

func New(code int, format string, args ...any) Error {
	return Error{Code: code, Err: fmt.Errorf(format, args...)}
}

func Code(code int) Error {
	return Error{Code: code}
}

func (e Error) Error() string {
	if e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (e Error) Unwrap() error {
	return e.Err
}

func (e Error) ExitCode() int {
	if e.Code == 0 {
		return 1
	}
	return e.Code
}

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
