package localhttp

// Resolves local HTTP definitions through the shared process pool and owns the
// protocol bridge used for each logical connector session.

import (
	"context"
	"fmt"
	"io"
	"sync"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/mcpgateway"
	"petris.dev/toby/internal/mcpgateway/httpbridge"
)

// Bridge serves one fresh logical HTTP session over a downstream stdio stream.
type Bridge interface {
	// Serve bridges one downstream stdio stream to HTTP.
	Serve(
		context.Context,
		io.ReadWriteCloser,
		httpbridge.Upstream,
	) error
}

var _ Bridge = (*httpbridge.Bridge)(nil)

// Resolver composes the shared local process pool with the protocol bridge.
type Resolver struct {
	pool   Pool
	bridge Bridge
	logger *diagnostic.Logger

	mu      sync.Mutex
	closing bool
}

var _ mcpgateway.BackendResolver = (*Resolver)(nil)

// NewResolver constructs the local HTTP target resolver.
func NewResolver(
	pool Pool,
	bridge Bridge,
	logger *diagnostic.Logger,
) (*Resolver, error) {
	if pool == nil {
		return nil, fmt.Errorf("local HTTP MCP process pool is required")
	}
	if bridge == nil {
		return nil, fmt.Errorf("local HTTP MCP bridge is required")
	}

	return &Resolver{
		pool:   pool,
		bridge: bridge,
		logger: logger,
	}, nil
}

// Class returns the local HTTP backend class.
func (*Resolver) Class() mcpgateway.TargetClass {
	return mcpgateway.TargetLocalHTTP
}

// Resolve canonicalizes the process definition without starting it.
func (r *Resolver) Resolve(
	ctx context.Context,
	request mcpgateway.TargetRequest,
) (mcpgateway.PreparedBackend, error) {
	if r == nil {
		return nil, fmt.Errorf("local HTTP MCP resolver is nil")
	}
	if ctx == nil {
		return nil, fmt.Errorf("local HTTP MCP resolve context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if request.Name == "" ||
		request.Spec.Type != mcpgateway.TargetLocal ||
		request.Spec.Transport != mcpgateway.TransportHTTP ||
		request.Spec.Image == "" ||
		len(request.Spec.Command) == 0 ||
		request.Spec.Endpoint == nil {
		return nil, fmt.Errorf("local HTTP MCP target request is incomplete")
	}

	r.mu.Lock()
	closing := r.closing
	r.mu.Unlock()
	if closing {
		return nil, fmt.Errorf("local HTTP MCP resolver is shutting down")
	}

	return &prepared{
		bridge:     r.bridge,
		pool:       r.pool,
		definition: definitionFromSpec(request.Spec),
		logger:     r.logger,
	}, nil
}

// Shutdown refuses new definitions and permanently shuts down the shared pool.
func (r *Resolver) Shutdown(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		return fmt.Errorf("local HTTP MCP shutdown context is nil")
	}

	r.mu.Lock()
	r.closing = true
	r.mu.Unlock()

	return r.pool.Shutdown(ctx)
}
