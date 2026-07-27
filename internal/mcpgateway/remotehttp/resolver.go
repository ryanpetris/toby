package remotehttp

// Resolves validated remote endpoints into per-run bridge targets without
// exposing their URLs or headers in descriptors.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/mcpgateway"
	"petris.dev/toby/internal/mcpgateway/httpbridge"
)

// Bridge serves one fresh logical HTTP session over a downstream stdio stream.
type Bridge interface {
	// Serve bridges one downstream stdio stream to remote HTTP.
	Serve(
		context.Context,
		io.ReadWriteCloser,
		httpbridge.Upstream,
	) error
}

var _ Bridge = (*httpbridge.Bridge)(nil)

// Resolver owns the shared HTTP bridge and refuses new sessions after agent
// shutdown begins.
type Resolver struct {
	bridge Bridge
	logger *diagnostic.Logger

	mu      sync.Mutex
	closing bool
}

var _ mcpgateway.BackendResolver = (*Resolver)(nil)

// NewResolver constructs the remote HTTP target resolver.
func NewResolver(
	bridge Bridge,
	logger *diagnostic.Logger,
) (*Resolver, error) {
	if bridge == nil {
		return nil, fmt.Errorf("remote MCP HTTP bridge is required")
	}

	return &Resolver{bridge: bridge, logger: logger}, nil
}

// Class returns the remote HTTP backend class.
func (*Resolver) Class() mcpgateway.TargetClass {
	return mcpgateway.TargetRemoteHTTP
}

// Resolve clones the already validated endpoint and secret headers.
func (r *Resolver) Resolve(
	ctx context.Context,
	request mcpgateway.TargetRequest,
) (mcpgateway.PreparedBackend, error) {
	if r == nil {
		return nil, fmt.Errorf("remote MCP HTTP resolver is nil")
	}
	if ctx == nil {
		return nil, fmt.Errorf("remote MCP HTTP resolve context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if request.Name == "" ||
		request.Spec.Type != mcpgateway.TargetRemote ||
		request.Spec.Transport != mcpgateway.TransportHTTP ||
		request.Spec.URL == "" {
		return nil, fmt.Errorf("remote MCP HTTP target request is incomplete")
	}

	r.mu.Lock()
	closing := r.closing
	r.mu.Unlock()
	if closing {
		return nil, fmt.Errorf("remote MCP HTTP resolver is shutting down")
	}

	headers := make(http.Header, len(request.Spec.Headers))
	for name, value := range request.Spec.Headers {
		headers.Set(name, value)
	}

	return &prepared{
		bridge: r.bridge,
		upstream: httpbridge.Upstream{
			Endpoint: request.Spec.URL,
			Headers:  headers,
		},
		logger: r.logger,
	}, nil
}

// Shutdown permanently refuses new remote sessions. Active sessions are
// revoked by their owning per-run gateway before this method is called.
func (r *Resolver) Shutdown(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		return fmt.Errorf("remote MCP HTTP shutdown context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	r.mu.Lock()
	r.closing = true
	r.mu.Unlock()

	return nil
}
