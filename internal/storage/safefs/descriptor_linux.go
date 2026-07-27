//go:build linux

package safefs

// Reports descriptor-close failures from cleanup paths that cannot return them.

import (
	"os"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/diagnostic"
)

func closeDescriptor(logger *diagnostic.Logger, descriptor int) {
	logger.DebugError(
		"close safe-filesystem descriptor",
		unix.Close(descriptor),
	)
}

func closeFile(logger *diagnostic.Logger, file *os.File) {
	if file == nil {
		return
	}
	logger.DebugError(
		"close safe-filesystem file",
		file.Close(),
		"path",
		file.Name(),
	)
}
