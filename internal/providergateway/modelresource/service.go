package modelresource

// Coordinates lazy models-route startup, coalesced retries, lease demand, and
// warm-idle teardown.

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"petris.dev/toby/internal/agent/protocol"
	"petris.dev/toby/internal/agent/resourcelease"
	"petris.dev/toby/internal/agent/resourcelog"
	modelsconfig "petris.dev/toby/internal/config/models"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/providergateway"
)

const (
	defaultIdleTimeout    = 10 * time.Minute
	defaultCleanupTimeout = 15 * time.Second
	defaultInitialRetry   = 250 * time.Millisecond
	defaultMaximumRetry   = 5 * time.Minute
	defaultModelsCacheTTL = 5 * time.Minute
)

// Options configures retained-generation lifecycle timing. Zero values use
// production defaults.
type Options struct {
	IdleTimeout    time.Duration
	CleanupTimeout time.Duration
	InitialRetry   time.Duration
	MaximumRetry   time.Duration
	CacheTTL       time.Duration
	Jitter         func(time.Duration) time.Duration
	Logger         *diagnostic.Logger
}

func (o Options) withDefaults() (Options, error) {
	if o.IdleTimeout < 0 ||
		o.CleanupTimeout < 0 ||
		o.InitialRetry < 0 ||
		o.MaximumRetry < 0 ||
		o.CacheTTL < 0 {
		return Options{}, fmt.Errorf(
			"models resource lifecycle durations must not be negative",
		)
	}
	if o.IdleTimeout == 0 {
		o.IdleTimeout = defaultIdleTimeout
	}
	if o.CleanupTimeout == 0 {
		o.CleanupTimeout = defaultCleanupTimeout
	}
	if o.InitialRetry == 0 {
		o.InitialRetry = defaultInitialRetry
	}
	if o.MaximumRetry == 0 {
		o.MaximumRetry = defaultMaximumRetry
	}
	if o.CacheTTL == 0 {
		o.CacheTTL = defaultModelsCacheTTL
	}
	if o.MaximumRetry < o.InitialRetry {
		return Options{}, fmt.Errorf(
			"models maximum retry delay must not be shorter than the initial delay",
		)
	}
	if o.Jitter == nil {
		o.Jitter = cryptoJitter
	}

	return o, nil
}

// Service owns retained Caddy routes keyed by models resource ID.
type Service struct {
	resolver modelsResolver
	options  Options
	logs     *resourcelog.Service
	logger   *diagnostic.Logger

	mu       sync.Mutex
	runtimes map[protocol.ResourceID]*runtimeState
	caches   map[protocol.ResourceID]*cacheState
	closing  bool
}

var _ resourcelease.RuntimeLifecycle = (*Service)(nil)
var _ resourcelease.RuntimeLister = (*Service)(nil)
var _ resourcelease.ModelsOperator = (*Service)(nil)

type modelsResolver interface {
	Acquire(
		context.Context,
		modelsconfig.Config,
		providergateway.ProgressReporter,
	) (modelsBackend, error)
	Shutdown(context.Context) error
}

type modelsBackend interface {
	Discover(context.Context) (map[string]any, error)
	Serve(context.Context, net.Conn) error
	Revoke()
	Release(context.Context) error
}

// New constructs a lazy models resource service.
func New(
	resolver *providergateway.Resolver,
	logs *resourcelog.Service,
	options Options,
) (*Service, error) {
	if resolver == nil {
		return nil, fmt.Errorf("models gateway resolver is required")
	}
	return newService(
		productionResolver{resolver: resolver},
		logs,
		options,
	)
}

func newService(
	resolver modelsResolver,
	logs *resourcelog.Service,
	options Options,
) (*Service, error) {
	if resolver == nil {
		return nil, fmt.Errorf("models gateway resolver is required")
	}
	if logs == nil {
		return nil, fmt.Errorf("models resource log service is required")
	}
	options, err := options.withDefaults()
	if err != nil {
		return nil, err
	}

	return &Service{
		resolver: resolver,
		options:  options,
		logs:     logs,
		logger:   options.Logger,
		runtimes: make(map[protocol.ResourceID]*runtimeState),
		caches:   make(map[protocol.ResourceID]*cacheState),
	}, nil
}

// Kind reports the models resource kind.
func (*Service) Kind() protocol.ResourceKind {
	return protocol.ResourceModels
}

// RuntimeResourceIDs returns opaque identities for retained or starting models
// generations. Registration-only dormant resources remain represented by the
// lease registry instead.
func (h *Service) RuntimeResourceIDs() []protocol.ResourceID {
	if h == nil {
		return nil
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	result := make([]protocol.ResourceID, 0, len(h.runtimes))
	for id, current := range h.runtimes {
		if current.backend != nil ||
			current.starting != nil ||
			current.waiters != 0 ||
			current.streams != 0 {
			result = append(result, id)
		}
	}

	return result
}

// LeaseAcquired records registration without opening or retaining a Caddy route.
func (h *Service) LeaseAcquired(resource resourcelease.Resolved) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closing {
		return
	}

	current := h.runtimes[resource.ID]
	if current == nil {
		current = &runtimeState{resource: resource}
		h.runtimes[resource.ID] = current
	}
	current.leases++
}

// LeaseReleased drops registration demand without waiting for idle cleanup.
func (h *Service) LeaseReleased(resource resourcelease.Resolved) {
	h.mu.Lock()
	current := h.runtimes[resource.ID]
	if current != nil && current.leases > 0 {
		current.leases--
		h.releaseDemandLocked(current)
	}
	h.mu.Unlock()
}
