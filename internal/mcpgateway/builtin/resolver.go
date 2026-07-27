package builtin

// Validates the serialized session snapshot and prepares a per-run built-in
// target without opening a protocol connection.

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/mcpgateway"
	"petris.dev/toby/internal/tobymcp"
)

// SessionDecoder strictly decodes the non-secret, data-only session snapshot
// supplied by the launch process.
type SessionDecoder func(
	json.RawMessage,
) (tobymcp.SessionSnapshot, error)

// Resolver owns no reusable process; it binds a fresh SDK server to every
// connector admitted by one acquired run target.
type Resolver struct {
	runner  *tobymcp.Runner
	decoder SessionDecoder
	logger  *diagnostic.Logger

	mu      sync.Mutex
	closing bool
}

var _ mcpgateway.BackendResolver = (*Resolver)(nil)

// NewResolver constructs the built-in target resolver.
func NewResolver(
	runner *tobymcp.Runner,
	decoder SessionDecoder,
	logger *diagnostic.Logger,
) (*Resolver, error) {
	if runner == nil {
		return nil, fmt.Errorf("built-in MCP runner is required")
	}
	if decoder == nil {
		return nil, fmt.Errorf("built-in MCP session decoder is required")
	}

	return &Resolver{
		runner:  runner,
		decoder: decoder,
		logger:  logger,
	}, nil
}

// Class returns the built-in backend class.
func (*Resolver) Class() mcpgateway.TargetClass {
	return mcpgateway.TargetBuiltin
}

// Resolve decodes and clones the run snapshot without serving a connection.
func (r *Resolver) Resolve(
	ctx context.Context,
	request mcpgateway.TargetRequest,
) (mcpgateway.PreparedBackend, error) {
	if r == nil {
		return nil, fmt.Errorf("built-in MCP resolver is nil")
	}
	if ctx == nil {
		return nil, fmt.Errorf("built-in MCP resolve context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if request.Name != mcpgateway.BuiltinTarget ||
		request.ResourceID == "" ||
		!hasCaller(request.Caller) {
		return nil, fmt.Errorf("built-in MCP target request is incomplete")
	}
	if len(request.Session) == 0 {
		return nil, fmt.Errorf("built-in MCP session snapshot is required")
	}

	r.mu.Lock()
	closing := r.closing
	r.mu.Unlock()
	if closing {
		return nil, fmt.Errorf("built-in MCP resolver is shutting down")
	}

	snapshot, err := r.decoder(
		append(json.RawMessage(nil), request.Session...),
	)
	if err != nil {
		return nil, fmt.Errorf("decode built-in MCP session snapshot: %w", err)
	}

	return &prepared{
		runner:   r.runner,
		caller:   request.Caller,
		snapshot: snapshot.Clone(),
		logger:   r.logger,
	}, nil
}

func hasCaller(caller any) bool {
	if caller == nil {
		return false
	}

	value := reflect.ValueOf(caller)
	switch value.Kind() {
	case reflect.Chan,
		reflect.Func,
		reflect.Interface,
		reflect.Map,
		reflect.Pointer,
		reflect.Slice:
		return !value.IsNil()
	default:
		return true
	}
}

// Shutdown permanently refuses new built-in target resolutions.
func (r *Resolver) Shutdown(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		return fmt.Errorf("built-in MCP shutdown context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	r.mu.Lock()
	r.closing = true
	r.mu.Unlock()

	return nil
}
