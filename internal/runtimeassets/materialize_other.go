//go:build !linux

package runtimeassets

// Reports that descriptor-safe runtime-asset materialization requires Linux.

import (
	"fmt"

	"petris.dev/toby/internal/storage/safefs"
)

// Materialize reports that exact runtime-asset sources require Linux.
func (r *Registry) Materialize(
	*safefs.Directory,
) (*Set, error) {
	return nil, fmt.Errorf(
		"%w: runtime asset materialization requires Linux",
		safefs.ErrUnsupported,
	)
}
