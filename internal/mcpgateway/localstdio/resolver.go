package localstdio

// Resolves local stdio definitions and tracks active per-connector process
// launches for agent lifecycle cleanup.

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/mcpgateway"
)

// Resolver owns the process launcher used for every local stdio connector.
type Resolver struct {
	launcher Launcher
	logger   *diagnostic.Logger

	mu       sync.Mutex
	acquired map[*acquired]struct{}
	closing  bool
}

var _ mcpgateway.BackendResolver = (*Resolver)(nil)

// NewResolver constructs the local stdio target resolver.
func NewResolver(
	launcher Launcher,
	logger *diagnostic.Logger,
) (*Resolver, error) {
	if launcher == nil {
		return nil, fmt.Errorf("local stdio MCP launcher is required")
	}

	return &Resolver{
		launcher: launcher,
		logger:   logger,
		acquired: make(map[*acquired]struct{}),
	}, nil
}

// Class returns the local stdio backend class.
func (*Resolver) Class() mcpgateway.TargetClass {
	return mcpgateway.TargetLocalStdio
}

// Resolve clones the validated process definition without launching it.
func (r *Resolver) Resolve(
	ctx context.Context,
	request mcpgateway.TargetRequest,
) (mcpgateway.PreparedBackend, error) {
	if r == nil {
		return nil, fmt.Errorf("local stdio MCP resolver is nil")
	}
	if ctx == nil {
		return nil, fmt.Errorf("local stdio MCP resolve context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if request.Name == "" ||
		request.Spec.Type != mcpgateway.TargetLocal ||
		request.Spec.Transport != mcpgateway.TransportStdio ||
		request.Spec.Image == "" ||
		len(request.Spec.Command) == 0 {
		return nil, fmt.Errorf("local stdio MCP target request is incomplete")
	}

	r.mu.Lock()
	closing := r.closing
	r.mu.Unlock()
	if closing {
		return nil, fmt.Errorf("local stdio MCP resolver is shutting down")
	}

	return &prepared{
		resolver: r,
		launch:   launchFromSpec(request.Spec),
	}, nil
}

// Shutdown refuses new targets, revokes all remaining acquisitions, and waits
// for their connector processes to reap.
func (r *Resolver) Shutdown(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		return fmt.Errorf("local stdio MCP shutdown context is nil")
	}

	r.mu.Lock()
	r.closing = true
	active := make([]*acquired, 0, len(r.acquired))
	for item := range r.acquired {
		active = append(active, item)
	}
	r.mu.Unlock()

	for _, item := range active {
		item.Revoke()
	}
	var shutdownErr error
	for _, item := range active {
		shutdownErr = errors.Join(shutdownErr, item.Release(ctx))
	}

	return shutdownErr
}

func (r *Resolver) register(item *acquired) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closing {
		return fmt.Errorf("local stdio MCP resolver is shutting down")
	}
	r.acquired[item] = struct{}{}

	return nil
}

func (r *Resolver) unregister(item *acquired) {
	r.mu.Lock()
	delete(r.acquired, item)
	r.mu.Unlock()
}
