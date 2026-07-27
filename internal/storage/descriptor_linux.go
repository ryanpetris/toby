//go:build linux

package storage

// Reports descriptor-close failures from seed cleanup paths that cannot return them.

import (
	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/diagnostic"
)

func closeDescriptor(logger *diagnostic.Logger, descriptor int) {
	logger.DebugError("close storage descriptor", unix.Close(descriptor))
}
