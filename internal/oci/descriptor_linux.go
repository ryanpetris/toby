//go:build linux

package oci

// Reports descriptor-close failures from OCI cleanup paths.

import (
	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/diagnostic"
)

func closeDescriptor(logger *diagnostic.Logger, descriptor int) {
	logger.DebugError("close OCI descriptor", unix.Close(descriptor))
}
