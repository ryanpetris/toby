//go:build linux

package bwrap

// Reports descriptor-close failures from sandbox cleanup paths.

import (
	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/diagnostic"
)

func closeDescriptor(logger *diagnostic.Logger, descriptor int) {
	logger.DebugError("close Bubblewrap descriptor", unix.Close(descriptor))
}
