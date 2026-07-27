//go:build !linux

package socketrelay

// Reports that descriptor-pinned Unix socket relays require Linux.

import (
	"context"
	"fmt"

	"petris.dev/toby/internal/diagnostic/warning"
	"petris.dev/toby/internal/storage/safefs"
)

// Start reports that run-owned socket relays require Linux.
func (r *Registry) Start(
	context.Context,
	*safefs.Directory,
	*warning.Service,
) (*Set, error) {
	return nil, fmt.Errorf(
		"%w: socket relays require Linux",
		safefs.ErrUnsupported,
	)
}
