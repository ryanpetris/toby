package providergateway

// Lazily opens the process-wide models gateway facilities on first use.

import (
	"context"
	"fmt"
	"sync"
)

// Resolver lazily owns the shared gateway used by agent models resources.
type Resolver struct {
	factory GatewayFactory
	options Options

	mu      sync.Mutex
	gateway *Gateway
	flight  *gatewayFlight
	closing bool

	shutdownOnce sync.Once
	shutdownDone chan struct{}
	shutdownErr  error
}

type gatewayFlight struct {
	done chan struct{}
	err  error
}

// NewResolver constructs an unopened models gateway resolver.
func NewResolver(
	factory GatewayFactory,
	options Options,
) (*Resolver, error) {
	if factory == nil {
		return nil, fmt.Errorf(
			"models gateway factory is required",
		)
	}
	normalized, err := options.normalized()
	if err != nil {
		return nil, err
	}

	return &Resolver{
		factory:      factory,
		options:      normalized,
		shutdownDone: make(chan struct{}),
	}, nil
}

// Shutdown refuses new work and closes an initialized gateway.
func (r *Resolver) Shutdown(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		return fmt.Errorf(
			"models gateway resolver shutdown context is nil",
		)
	}

	r.shutdownOnce.Do(func() {
		r.mu.Lock()
		r.closing = true
		flight := r.flight
		r.mu.Unlock()

		go r.finishShutdown(flight)
	})

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.shutdownDone:
		return r.shutdownErr
	}
}

func (r *Resolver) gatewayFor(
	ctx context.Context,
	progress ProgressReporter,
) (*Gateway, error) {
	for {
		r.mu.Lock()
		if r.closing {
			r.mu.Unlock()
			return nil, fmt.Errorf(
				"models gateway resolver is shutting down",
			)
		}
		if r.gateway != nil {
			gateway := r.gateway
			r.mu.Unlock()
			return gateway, nil
		}
		if r.flight != nil {
			flight := r.flight
			r.mu.Unlock()

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

		flight := &gatewayFlight{done: make(chan struct{})}
		r.flight = flight
		factory := r.factory
		r.mu.Unlock()

		gateway, err := factory(ctx, progress)
		if err == nil && gateway == nil {
			err = fmt.Errorf(
				"models gateway factory returned nil",
			)
		}

		r.mu.Lock()
		if err == nil {
			r.gateway = gateway
			if r.closing {
				err = fmt.Errorf(
					"models gateway resolver is shutting down",
				)
			}
		}
		flight.err = err
		r.flight = nil
		close(flight.done)
		r.mu.Unlock()

		if err != nil {
			return nil, err
		}
		return gateway, nil
	}
}

func (r *Resolver) finishShutdown(flight *gatewayFlight) {
	defer close(r.shutdownDone)

	if flight != nil {
		<-flight.done
	}

	r.mu.Lock()
	gateway := r.gateway
	r.gateway = nil
	r.mu.Unlock()
	if gateway == nil {
		return
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		r.options.CleanupTimeout,
	)
	defer cancel()
	r.shutdownErr = gateway.Shutdown(ctx)
}
