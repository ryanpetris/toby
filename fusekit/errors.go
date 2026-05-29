package fusekit

import (
	"errors"
	"syscall"
)

func errnoError(errno syscall.Errno) error {
	if errno == 0 {
		return nil
	}
	return errno
}

// ErrnoOf converts an operation error to a syscall errno.
func ErrnoOf(err error) syscall.Errno {
	if err == nil {
		return 0
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno
	}
	return syscall.EIO
}

func isErrno(err error, errno syscall.Errno) bool {
	return ErrnoOf(err) == errno
}
