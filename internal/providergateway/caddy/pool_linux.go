//go:build linux

package caddy

// Lazily opens the per-user OCI/Bubblewrap facilities and exposes one shared
// Caddy resource generation to the models gateway.

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"petris.dev/toby/internal/agent/resource"
	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/providergateway"
)

const defaultIdleTimeout = 10 * time.Minute

// AuthorizationSource opens a caller-owned descriptor for the exact protected
// authorization socket.
type AuthorizationSource func() (*os.File, error)

// Pool is one lazy Caddy resource registry.
type Pool struct {
	paths       config.Paths
	builder     *resource.Builder
	image       string
	authPath    string
	authSource  AuthorizationSource
	options     PoolOptions
	diagnostics *diagnostic.Service
	logger      *diagnostic.Logger

	mu      sync.Mutex
	runtime *nativeRuntime
	flight  *poolFlight
	closing bool

	shutdownOnce sync.Once
	shutdownDone chan struct{}
	shutdownErr  error
}

var _ providergateway.Pool = (*Pool)(nil)

// PoolOptions bounds process lifetime and readiness.
type PoolOptions struct {
	IdleTimeout      time.Duration
	ReadinessTimeout time.Duration
	ReadinessPoll    time.Duration
}

func (o PoolOptions) normalized() (PoolOptions, error) {
	if o.IdleTimeout < 0 ||
		o.ReadinessTimeout < 0 ||
		o.ReadinessPoll < 0 {
		return PoolOptions{}, fmt.Errorf(
			"caddy pool durations must not be negative",
		)
	}
	if o.IdleTimeout == 0 {
		o.IdleTimeout = defaultIdleTimeout
	}
	if o.ReadinessTimeout == 0 {
		o.ReadinessTimeout = 15 * time.Second
	}
	if o.ReadinessPoll == 0 {
		o.ReadinessPoll = 20 * time.Millisecond
	}
	if o.ReadinessPoll > o.ReadinessTimeout {
		return PoolOptions{}, fmt.Errorf(
			"caddy readiness interval exceeds its timeout",
		)
	}

	return o, nil
}

// NewPool constructs an unopened per-user Caddy registry.
func NewPool(
	paths config.Paths,
	builder *resource.Builder,
	imageReference string,
	authPath string,
	authSource AuthorizationSource,
	options PoolOptions,
	diagnostics *diagnostic.Service,
) (*Pool, error) {
	if builder == nil {
		return nil, fmt.Errorf(
			"caddy resource builder is required",
		)
	}
	normalizedImage, err := normalizeImage(imageReference)
	if err != nil {
		return nil, err
	}
	normalizedOptions, err := options.normalized()
	if err != nil {
		return nil, err
	}
	if authPath == "" || authSource == nil {
		return nil, fmt.Errorf(
			"caddy authorization socket capability is required",
		)
	}

	return &Pool{
		paths:        paths,
		builder:      builder,
		image:        normalizedImage,
		authPath:     authPath,
		authSource:   authSource,
		options:      normalizedOptions,
		diagnostics:  diagnostics,
		logger:       diagnostics.Logger("provider.caddy"),
		shutdownDone: make(chan struct{}),
	}, nil
}

// Acquire retains one ready shared generation.
func (p *Pool) Acquire(
	ctx context.Context,
	progress providergateway.ProgressReporter,
) (providergateway.Generation, error) {
	if p == nil {
		return nil, fmt.Errorf("caddy pool is nil")
	}
	if ctx == nil {
		return nil, fmt.Errorf("caddy acquire context is nil")
	}

	prepareOperation := startProgress(
		progress,
		"Preparing Caddy runtime",
	)
	native, err := p.runtimeFor(ctx, progress)
	if err != nil {
		prepareOperation.Fail("Caddy runtime preparation failed")
		return nil, err
	}
	prepareOperation.Complete("Caddy runtime ready")

	startOperation := startProgress(
		progress,
		"Starting Caddy process",
	)
	lease, err := native.registry.AcquireWithPolicy(
		withProgress(ctx, progress),
		native.key,
		resource.AcquisitionPolicy{
			IdleTimeout: p.options.IdleTimeout,
		},
	)
	if err != nil {
		startOperation.Fail("Caddy process startup failed")
		return nil, err
	}
	startOperation.Complete("Caddy process ready")

	instance, err := lease.Instance()
	if err != nil {
		lease.Release()
		return nil, err
	}
	process, ok := instance.(*Instance)
	if !ok {
		lease.Release()
		return nil, fmt.Errorf(
			"caddy registry returned an invalid generation",
		)
	}

	return &generation{
		lease:    lease,
		instance: process,
	}, nil
}

// Shutdown permanently closes an initialized registry and its native stores.
func (p *Pool) Shutdown(ctx context.Context) error {
	if p == nil {
		return nil
	}
	if ctx == nil {
		return fmt.Errorf("caddy pool shutdown context is nil")
	}

	p.shutdownOnce.Do(func() {
		p.mu.Lock()
		p.closing = true
		flight := p.flight
		p.mu.Unlock()

		go p.finishShutdown(flight)
	})

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.shutdownDone:
		return p.shutdownErr
	}
}

func (p *Pool) runtimeFor(
	ctx context.Context,
	progress providergateway.ProgressReporter,
) (*nativeRuntime, error) {
	for {
		p.mu.Lock()
		if p.closing {
			p.mu.Unlock()
			return nil, fmt.Errorf("caddy pool is shutting down")
		}
		if p.runtime != nil {
			native := p.runtime
			p.mu.Unlock()
			return native, nil
		}
		if p.flight != nil {
			flight := p.flight
			p.mu.Unlock()
			waitOperation := startProgress(
				progress,
				"Waiting for Caddy runtime preparation",
			)
			select {
			case <-ctx.Done():
				waitOperation.Fail(
					"Caddy runtime preparation wait failed",
				)
				return nil, ctx.Err()
			case <-flight.done:
				if flight.err != nil {
					waitOperation.Fail(
						"Caddy runtime preparation failed",
					)
					return nil, flight.err
				}
				waitOperation.Complete(
					"Caddy runtime preparation finished",
				)
				continue
			}
		}

		flight := &poolFlight{done: make(chan struct{})}
		p.flight = flight
		p.mu.Unlock()

		native, err := p.openRuntime(ctx, progress)

		p.mu.Lock()
		if err == nil {
			p.runtime = native
			if p.closing {
				err = fmt.Errorf("caddy pool is shutting down")
			}
		}
		flight.err = err
		p.flight = nil
		close(flight.done)
		p.mu.Unlock()

		if err != nil {
			return nil, err
		}
		return native, nil
	}
}
