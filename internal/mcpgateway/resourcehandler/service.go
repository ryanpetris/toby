package resourcehandler

// Coordinates first-use startup, retry backoff, lease registration, and warm-idle
// teardown for agent MCP resource streams.

import (
	"context"
	"fmt"
	"sync"
	"time"

	"petris.dev/toby/internal/agent/protocol"
	"petris.dev/toby/internal/agent/resourcelease"
	"petris.dev/toby/internal/agent/resourcelog"
	agentserver "petris.dev/toby/internal/agent/server"
	"petris.dev/toby/internal/config/mcpresource"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/mcpgateway"
)

const (
	defaultIdleTimeout    = 10 * time.Minute
	defaultCleanupTimeout = 15 * time.Second
	defaultInitialRetry   = 250 * time.Millisecond
	defaultMaximumRetry   = 5 * time.Minute
)

// Options configures retained-generation lifecycle timing. Zero values use
// production defaults.
type Options struct {
	IdleTimeout    time.Duration
	CleanupTimeout time.Duration
	InitialRetry   time.Duration
	MaximumRetry   time.Duration
	Jitter         func(time.Duration) time.Duration
	Logger         *diagnostic.Logger
}

func (o Options) withDefaults() (Options, error) {
	if o.IdleTimeout < 0 ||
		o.CleanupTimeout < 0 ||
		o.InitialRetry < 0 ||
		o.MaximumRetry < 0 {
		return Options{}, fmt.Errorf(
			"MCP resource lifecycle durations must not be negative",
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
	if o.MaximumRetry < o.InitialRetry {
		return Options{}, fmt.Errorf(
			"MCP maximum retry delay must not be shorter than the initial delay",
		)
	}
	if o.Jitter == nil {
		o.Jitter = cryptoJitter
	}

	return o, nil
}

// Service owns retained MCP backend generations keyed by resource ID.
type Service struct {
	backends map[mcpgateway.TargetClass]mcpgateway.BackendResolver
	ordered  []mcpgateway.BackendResolver
	options  Options
	logs     *resourcelog.Service
	logger   *diagnostic.Logger

	mu       sync.Mutex
	runtimes map[protocol.ResourceID]*runtimeState
	closing  bool
}

var _ resourcelease.RuntimeLifecycle = (*Service)(nil)
var _ resourcelease.RuntimeLister = (*Service)(nil)

// New constructs a service from the closed MCP backend resolver set.
func New(
	backends []mcpgateway.BackendResolver,
	logs *resourcelog.Service,
	options Options,
) (*Service, error) {
	if logs == nil {
		return nil, fmt.Errorf("MCP resource log service is required")
	}
	options, err := options.withDefaults()
	if err != nil {
		return nil, err
	}

	byClass := make(
		map[mcpgateway.TargetClass]mcpgateway.BackendResolver,
		len(backends),
	)
	for index, backend := range backends {
		if backend == nil {
			return nil, fmt.Errorf("MCP backend resolver %d is nil", index)
		}
		class := backend.Class()
		if _, duplicate := byClass[class]; duplicate {
			return nil, fmt.Errorf(
				"MCP backend class %q is registered more than once",
				class,
			)
		}
		byClass[class] = backend
	}
	for _, class := range []mcpgateway.TargetClass{
		mcpgateway.TargetBuiltin,
		mcpgateway.TargetLocalHTTP,
		mcpgateway.TargetLocalStdio,
		mcpgateway.TargetRemoteHTTP,
	} {
		if byClass[class] == nil {
			return nil, fmt.Errorf(
				"MCP backend class %q is not registered",
				class,
			)
		}
	}

	return &Service{
		backends: byClass,
		ordered:  append([]mcpgateway.BackendResolver(nil), backends...),
		logs:     logs,
		options:  options,
		logger:   options.Logger,
		runtimes: make(map[protocol.ResourceID]*runtimeState),
	}, nil
}

// Kind reports the one resource kind served by this service.
func (*Service) Kind() protocol.ResourceKind {
	return protocol.ResourceMCP
}

// RuntimeResourceIDs returns opaque identities for retained or starting MCP
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
		if current.acquired != nil ||
			current.starting != nil ||
			current.waiters != 0 ||
			current.streams != 0 {
			result = append(result, id)
		}
	}

	return result
}

// LeaseAcquired records registration without starting or retaining a backend.
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
	} else if current.resource.Kind != resource.Kind {
		return
	}
	current.leases++
}

// LeaseReleased drops registration and removes dormant state after the final
// lease. Active generations use stream demand and warm-idle retention.
func (h *Service) LeaseReleased(resource resourcelease.Resolved) {
	h.mu.Lock()
	current := h.runtimes[resource.ID]
	if current != nil && current.leases > 0 {
		current.leases--
		h.releaseDemandLocked(current)
	}
	h.mu.Unlock()
}

// Open joins or starts the retained backend generation and reserves one stream.
func (h *Service) Open(
	ctx context.Context,
	request resourcelease.StreamRequest,
) (agentserver.ResourceStream, error) {
	if ctx == nil {
		return nil, fmt.Errorf("MCP resource stream context is nil")
	}
	configuration, ok := request.Resource.Configuration.(mcpresource.Config)
	if !ok {
		return nil, fmt.Errorf(
			"MCP resource has configuration type %T",
			request.Resource.Configuration,
		)
	}

	h.mu.Lock()
	current := h.runtimes[request.Resource.ID]
	if current == nil || current.leases == 0 {
		h.mu.Unlock()
		return nil, fmt.Errorf("MCP resource has no active lease")
	}
	current.waiters++
	if current.idle != nil {
		current.idle.Stop()
		current.idle = nil
	}
	h.mu.Unlock()
	waiting := true
	defer func() {
		if !waiting {
			return
		}
		h.mu.Lock()
		current := h.runtimes[request.Resource.ID]
		if current != nil && current.waiters > 0 {
			current.waiters--
			h.releaseDemandLocked(current)
		}
		h.mu.Unlock()
	}()

	for {
		h.mu.Lock()
		if h.closing {
			h.mu.Unlock()
			return nil, fmt.Errorf("MCP resource service is shutting down")
		}
		current := h.runtimes[request.Resource.ID]
		if current == nil {
			h.mu.Unlock()
			return nil, fmt.Errorf("MCP resource lease was released")
		}
		if current.idle != nil {
			current.idle.Stop()
			current.idle = nil
		}
		if current.acquired != nil {
			current.waiters--
			current.streams++
			waiting = false
			acquired := current.acquired
			h.mu.Unlock()
			return newStream(h, request.Resource.ID, acquired)
		}
		if current.starting != nil {
			wait := current.starting
			h.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-wait:
				continue
			}
		}
		if current.lastErr != nil &&
			current.acquired == nil &&
			!mcpgateway.RetryableStart(current.lastErr) {
			err := current.lastErr
			h.mu.Unlock()
			return nil, err
		}
		if delay := time.Until(current.retryAt); delay > 0 {
			h.mu.Unlock()
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
				continue
			}
		}

		current.starting = make(chan struct{})
		h.mu.Unlock()

		acquired, err := h.start(ctx, configuration, request)
		if err != nil && acquired != nil {
			h.releaseBackend(acquired)
			acquired = nil
		}

		h.mu.Lock()
		current = h.runtimes[request.Resource.ID]
		if current == nil {
			h.mu.Unlock()
			if acquired != nil {
				h.releaseBackend(acquired)
			}
			return nil, fmt.Errorf("MCP resource was released during startup")
		}
		wait := current.starting
		current.starting = nil
		if err == nil && !h.closing {
			current.acquired = acquired
			current.lastErr = nil
			current.failures = 0
			current.retryAt = time.Time{}
		} else {
			if err == nil {
				err = fmt.Errorf("MCP resource service is shutting down")
			}
			current.lastErr = err
			if mcpgateway.RetryableStart(err) {
				current.failures++
				current.retryAt = time.Now().Add(
					h.retryDelay(current.failures),
				)
			} else {
				current.failures = 0
				current.retryAt = time.Time{}
			}
		}
		close(wait)
		h.releaseDemandLocked(current)
		h.mu.Unlock()

		if err != nil {
			if ctx.Err() != nil {
				return nil, err
			}
			if !mcpgateway.RetryableStart(err) {
				return nil, err
			}
			continue
		}
	}
}
