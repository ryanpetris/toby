//go:build linux

package socketrelay

// Starts Linux relays beneath one exact private run-runtime directory.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/sandbox/bwrap"
	"petris.dev/toby/internal/sandbox/mount"
	"petris.dev/toby/internal/storage/safefs"
)

// Start verifies host access before creating run-scoped relay endpoints. An
// empty registry has no runtime authority and returns a nil set.
func (r *Registry) Start(
	ctx context.Context,
	root *safefs.Directory,
	logger *diagnostic.Logger,
) (*Set, error) {
	if r == nil {
		return nil, fmt.Errorf("socket relay registry is nil")
	}
	if len(r.requests) == 0 {
		return nil, nil
	}
	if ctx == nil {
		return nil, fmt.Errorf("start socket relays: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	result := &Set{
		relays:  make([]*relay, 0, len(r.requests)),
		assets:  make([]bwrap.RuntimeAsset, 0, len(r.requests)),
		sources: make(map[string]*os.File, len(r.requests)),
		logger:  logger,
	}
	for index, request := range r.requests {
		name := fmt.Sprintf(".relay-%06d.sock", index)
		current, err := newRelay(
			ctx,
			root,
			logger,
			name,
			request.HostSocket,
		)
		if err != nil {
			logger.DebugError(
				"close socket relays after startup failed",
				result.Close(),
			)
			return nil, fmt.Errorf(
				"start relay for %q: %w",
				request.SandboxSocket,
				err,
			)
		}
		result.relays = append(result.relays, current)

		source, err := current.File()
		if err != nil {
			logger.DebugError(
				"close socket relays after capability retention failed",
				result.Close(),
			)
			return nil, fmt.Errorf(
				"retain relay for %q: %w",
				request.SandboxSocket,
				err,
			)
		}
		result.sources[request.SandboxSocket] = source
		result.assets = append(result.assets, bwrap.RuntimeAsset{
			HostPath: filepath.Join(root.Path(), name),
			Target:   request.SandboxSocket,
			Access:   mount.AccessDev,
		})
	}

	return result, nil
}
