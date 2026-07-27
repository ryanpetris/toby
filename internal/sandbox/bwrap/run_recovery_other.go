//go:build !linux

package bwrap

// Fails closed where exact Bubblewrap run recovery is unavailable.

import (
	"fmt"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/storage/safefs"
)

func recoverPublishedRuns(
	*safefs.Directory,
	RunStorageLimits,
	*diagnostic.Logger,
) error {
	return fmt.Errorf("%w: Bubblewrap run recovery requires Linux", safefs.ErrUnsupported)
}
