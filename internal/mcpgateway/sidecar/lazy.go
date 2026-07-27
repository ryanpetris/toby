package sidecar

// Initializes agent-side native process facilities only when a configured
// local MCP first needs them, and owns their process-lifetime cleanup.

import (
	"context"
	"fmt"
	"io"
	"sync"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/mcpgateway"
)

// Runtime is one independently closable native sidecar facility.
type Runtime interface {
	Provider
	io.Closer
}

// Factory opens the native sidecar facility under the caller's bounded
// operation context.
type Factory func(context.Context) (Runtime, error)

// Lazy defers native Bubblewrap runtime and persistent-store creation until
// the first local sidecar operation.
type Lazy struct {
	factory Factory
	logger  *diagnostic.Logger

	mu      sync.Mutex
	runtime Runtime
	flight  *runtimeFlight
	closed  bool
}

var _ Runtime = (*Lazy)(nil)

type runtimeFlight struct {
	done chan struct{}
	err  error
}

func newLazy(
	factory Factory,
	logger *diagnostic.Logger,
) (*Lazy, error) {
	if factory == nil {
		return nil, fmt.Errorf("sidecar runtime factory is required")
	}

	return &Lazy{
		factory: factory,
		logger:  logger,
	}, nil
}

// Resolve lazily delegates immutable image planning.
func (l *Lazy) Resolve(
	ctx context.Context,
	definition Definition,
	progress mcpgateway.ProgressReporter,
) (Metadata, error) {
	runtime, err := l.get(ctx)
	if err != nil {
		return Metadata{}, err
	}

	return runtime.Resolve(ctx, definition, progress)
}

// Prepare lazily delegates one exact launch preparation.
func (l *Lazy) Prepare(
	ctx context.Context,
	definition Definition,
	progress mcpgateway.ProgressReporter,
) (*Prepared, error) {
	runtime, err := l.get(ctx)
	if err != nil {
		return nil, err
	}

	return runtime.Prepare(ctx, definition, progress)
}

// PinMounts lazily delegates exact mount capability retention.
func (l *Lazy) PinMounts(
	ctx context.Context,
	definition []mcpgateway.Mount,
) (*MountCapabilities, error) {
	runtime, err := l.get(ctx)
	if err != nil {
		return nil, err
	}

	return runtime.PinMounts(ctx, definition)
}

// PreparePinned lazily delegates preparation from retained mount
// capabilities.
func (l *Lazy) PreparePinned(
	ctx context.Context,
	definition Definition,
	mounts *MountCapabilities,
	progress mcpgateway.ProgressReporter,
) (*Prepared, error) {
	runtime, err := l.get(ctx)
	if err != nil {
		return nil, err
	}

	return runtime.PreparePinned(ctx, definition, mounts, progress)
}

// Close permanently refuses new operations and closes an initialized runtime.
func (l *Lazy) Close() error {
	if l == nil {
		return nil
	}

	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	runtime := l.runtime
	l.runtime = nil
	flight := l.flight
	l.mu.Unlock()

	if flight != nil {
		<-flight.done
	}
	if runtime == nil {
		return nil
	}

	l.logger.DebugError(
		"close lazy sidecar runtime",
		runtime.Close(),
	)
	return nil
}

func (l *Lazy) get(ctx context.Context) (Runtime, error) {
	if l == nil {
		return nil, fmt.Errorf("lazy sidecar runtime is nil")
	}
	if ctx == nil {
		return nil, fmt.Errorf("sidecar runtime context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	for {
		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			return nil, fmt.Errorf("sidecar runtime is closed")
		}
		if l.runtime != nil {
			runtime := l.runtime
			l.mu.Unlock()
			return runtime, nil
		}
		if l.flight != nil {
			flight := l.flight
			l.mu.Unlock()

			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-flight.done:
				if flight.err != nil {
					return nil, flight.err
				}
				continue
			}
		}

		flight := &runtimeFlight{done: make(chan struct{})}
		l.flight = flight
		factory := l.factory
		l.mu.Unlock()

		runtime, err := factory(ctx)
		if err == nil && isNilContract(runtime) {
			err = fmt.Errorf("sidecar runtime factory returned nil")
			runtime = nil
		}

		l.mu.Lock()
		if l.closed && runtime != nil {
			l.logger.DebugError(
				"close sidecar runtime opened during shutdown",
				runtime.Close(),
			)
			runtime = nil
		}
		if err == nil {
			l.runtime = runtime
		}
		flight.err = err
		l.flight = nil
		close(flight.done)
		l.mu.Unlock()

		if err != nil {
			return nil, err
		}
		return runtime, nil
	}
}
