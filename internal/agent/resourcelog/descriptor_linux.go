//go:build linux

package resourcelog

// Reports descriptor-close failures from resource-log cleanup paths.

import (
	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/diagnostic"
)

func closeDescriptor(logger *diagnostic.Logger, descriptor int) {
	logger.DebugError(
		"close resource log descriptor",
		unix.Close(descriptor),
	)
}
