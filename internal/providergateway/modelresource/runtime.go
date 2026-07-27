package modelresource

// Owns retained models gateway generations, retry backoff, and idle teardown.

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	"petris.dev/toby/internal/agent/protocol"
	"petris.dev/toby/internal/agent/resourcelease"
	"petris.dev/toby/internal/agent/resourceprogress"
	agentserver "petris.dev/toby/internal/agent/server"
	modelsconfig "petris.dev/toby/internal/config/models"
	"petris.dev/toby/internal/providergateway"
)

type runtimeState struct {
	resource resourcelease.Resolved
	leases   int
	waiters  int
	streams  int

	backend  modelsBackend
	starting chan struct{}
	failures uint32
	retryAt  time.Time
	idle     *time.Timer
}

type productionResolver struct {
	resolver *providergateway.Resolver
}

func (r productionResolver) Acquire(
	ctx context.Context,
	configuration modelsconfig.Config,
	progress providergateway.ProgressReporter,
) (modelsBackend, error) {
	return r.resolver.AcquireModels(ctx, configuration, progress)
}

func (r productionResolver) Shutdown(ctx context.Context) error {
	return r.resolver.Shutdown(ctx)
}

// Open joins or starts one route generation and reserves a stream.
func (h *Service) Open(
	ctx context.Context,
	request resourcelease.StreamRequest,
) (agentserver.ResourceStream, error) {
	if ctx == nil {
		return nil, fmt.Errorf("models resource stream context is nil")
	}
	configuration, ok := request.Resource.Configuration.(modelsconfig.Config)
	if !ok {
		return nil, fmt.Errorf(
			"models resource has configuration type %T",
			request.Resource.Configuration,
		)
	}

	h.mu.Lock()
	current := h.runtimes[request.Resource.ID]
	if current == nil || current.leases == 0 {
		h.mu.Unlock()
		return nil, fmt.Errorf("models resource has no active lease")
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
			return nil, fmt.Errorf(
				"models resource service is shutting down",
			)
		}
		current := h.runtimes[request.Resource.ID]
		if current == nil {
			h.mu.Unlock()
			return nil, fmt.Errorf("models resource lease was released")
		}
		if current.backend != nil {
			current.waiters--
			current.streams++
			waiting = false
			backend := current.backend
			h.mu.Unlock()
			return &stream{
				service: h,
				id:      request.Resource.ID,
				backend: backend,
			}, nil
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

		progress := resourceprogress.New(
			h.logs,
			h.logger,
			protocol.ResourceModels,
			request.Resource.ID,
		)
		var backend modelsBackend
		backend, err := h.resolver.Acquire(
			ctx,
			configuration,
			progress,
		)
		if err == nil && backend == nil {
			err = fmt.Errorf(
				"models gateway resolver returned no backend",
			)
		}
		progress.Finish(err)
		if err != nil && backend != nil {
			h.releaseBackend(backend)
			backend = nil
		}

		h.mu.Lock()
		current = h.runtimes[request.Resource.ID]
		if current == nil {
			h.mu.Unlock()
			if backend != nil {
				h.releaseBackend(backend)
			}
			return nil, fmt.Errorf(
				"models resource was released during startup",
			)
		}
		wait := current.starting
		current.starting = nil
		if err == nil && !h.closing {
			current.backend = backend
			current.failures = 0
			current.retryAt = time.Time{}
		} else {
			if err == nil {
				err = fmt.Errorf(
					"models resource service is shutting down",
				)
			}
			current.failures++
			current.retryAt = time.Now().Add(
				h.retryDelay(current.failures),
			)
		}
		close(wait)
		h.releaseDemandLocked(current)
		h.mu.Unlock()

		if err != nil {
			continue
		}
	}
}

// Shutdown synchronously revokes all routes and performs bounded cleanup.
func (h *Service) Shutdown(ctx context.Context) error {
	if h == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	h.mu.Lock()
	if h.closing {
		h.mu.Unlock()
		return nil
	}
	h.closing = true
	active := make(
		[]modelsBackend,
		0,
		len(h.runtimes),
	)
	for _, current := range h.runtimes {
		if current.idle != nil {
			current.idle.Stop()
		}
		if current.backend != nil {
			current.backend.Revoke()
			active = append(active, current.backend)
		}
	}
	clear(h.runtimes)
	for _, cache := range h.caches {
		if cache.timer != nil {
			cache.timer.Stop()
		}
		if cache.flight != nil {
			close(cache.flight.done)
		}
	}
	clear(h.caches)
	h.mu.Unlock()

	for _, backend := range active {
		if err := backend.Release(ctx); err != nil {
			h.logger.DebugError("release models backend", err)
		}
	}

	if err := h.resolver.Shutdown(ctx); err != nil {
		h.logger.DebugError("shut down models resolver", err)
		return err
	}
	return nil
}

func (h *Service) releaseStream(id protocol.ResourceID) {
	h.mu.Lock()
	current := h.runtimes[id]
	if current != nil && current.streams > 0 {
		current.streams--
		h.releaseDemandLocked(current)
	}
	h.mu.Unlock()
}

func (h *Service) releaseDemandLocked(current *runtimeState) {
	if current == nil ||
		current.waiters != 0 ||
		current.streams != 0 ||
		current.starting != nil {
		return
	}
	if current.backend == nil {
		if current.leases == 0 {
			delete(h.runtimes, current.resource.ID)
		}
		return
	}
	if current.idle != nil {
		return
	}

	id := current.resource.ID
	current.idle = time.AfterFunc(h.options.IdleTimeout, func() {
		h.expireIdle(id, current)
	})
}

func (h *Service) expireIdle(
	id protocol.ResourceID,
	expected *runtimeState,
) {
	h.mu.Lock()
	current := h.runtimes[id]
	if current != expected ||
		current.waiters != 0 ||
		current.streams != 0 ||
		current.starting != nil ||
		current.backend == nil {
		h.mu.Unlock()
		return
	}
	backend := current.backend
	current.backend = nil
	current.idle = nil
	current.failures = 0
	current.retryAt = time.Time{}
	if current.leases == 0 {
		delete(h.runtimes, id)
	}
	h.mu.Unlock()

	if backend != nil {
		backend.Revoke()
		h.releaseBackend(backend)
	}
}

func (h *Service) retryDelay(failures uint32) time.Duration {
	delay := h.options.InitialRetry
	for count := uint32(1); count < failures; count++ {
		if delay >= h.options.MaximumRetry/2 {
			delay = h.options.MaximumRetry
			break
		}
		delay *= 2
	}
	delay = h.options.Jitter(delay)
	if delay > h.options.MaximumRetry {
		return h.options.MaximumRetry
	}
	if delay < 0 {
		return 0
	}

	return delay
}

func cryptoJitter(delay time.Duration) time.Duration {
	var random [1]byte
	if _, err := rand.Read(random[:]); err == nil {
		factor := 0.75 + float64(random[0])/510
		delay = time.Duration(float64(delay) * factor)
	}

	return delay
}

func (h *Service) releaseBackend(
	backend modelsBackend,
) {
	ctx, cancel := context.WithTimeout(
		context.Background(),
		h.options.CleanupTimeout,
	)
	defer cancel()
	if err := backend.Release(ctx); err != nil {
		h.logger.DebugError("release models backend", err)
	}
}
