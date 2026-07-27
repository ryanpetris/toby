//go:build linux

package socket

// Reports descriptor-close failures from socket-lock cleanup paths.

import (
	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/diagnostic"
)

func closeDescriptor(logger *diagnostic.Logger, descriptor int) {
	logger.DebugError(
		"close agent socket descriptor",
		unix.Close(descriptor),
	)
}
