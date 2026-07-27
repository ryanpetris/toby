package resourcehandler

// Owns retained MCP backend generations, retry backoff, and idle teardown.

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"petris.dev/toby/internal/agent/protocol"
	"petris.dev/toby/internal/agent/resourcelease"
	"petris.dev/toby/internal/agent/resourceprogress"
	"petris.dev/toby/internal/config/mcpresource"
	"petris.dev/toby/internal/mcpgateway"
)

type runtimeState struct {
	resource resourcelease.Resolved

	leases  int
	waiters int
	streams int

	acquired mcpgateway.AcquiredBackend
	starting chan struct{}
	lastErr  error
	failures uint32
	retryAt  time.Time
	idle     *time.Timer
}

func (h *Service) start(
	ctx context.Context,
	configuration mcpresource.Config,
	request resourcelease.StreamRequest,
) (
	result mcpgateway.AcquiredBackend,
	returnErr error,
) {
	progress := resourceprogress.New(
		h.logs,
		h.logger,
		protocol.ResourceMCP,
		request.Resource.ID,
	)
	defer func() {
		progress.Finish(returnErr)
	}()

	requestDefinition, class, err := targetRequest(
		configuration,
		request,
	)
	if err != nil {
		return nil, err
	}
	resolver := h.backends[class]
	if resolver == nil {
		return nil, fmt.Errorf(
			"MCP backend class %q is unavailable",
			class,
		)
	}
	prepared, err := resolver.Resolve(ctx, requestDefinition)
	if err != nil {
		return nil, err
	}
	if prepared == nil {
		return nil, fmt.Errorf("MCP backend resolver returned nil")
	}
	acquired, err := prepared.Acquire(ctx, progress)
	if err != nil {
		return nil, err
	}
	if acquired == nil || acquired.Target() == nil {
		if acquired != nil {
			h.releaseBackend(acquired)
		}
		return nil, fmt.Errorf("MCP backend returned no connector target")
	}

	return acquired, nil
}

// Shutdown synchronously revokes every retained backend and starts bounded
// cleanup.
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
	active := make([]mcpgateway.AcquiredBackend, 0, len(h.runtimes))
	for _, current := range h.runtimes {
		if current.idle != nil {
			current.idle.Stop()
		}
		if current.acquired != nil {
			current.acquired.Revoke()
			active = append(active, current.acquired)
		}
	}
	clear(h.runtimes)
	h.mu.Unlock()

	for _, acquired := range active {
		if err := acquired.Release(ctx); err != nil {
			h.logger.DebugError("release MCP backend", err)
		}
	}
	var shutdownErr error
	for _, backend := range h.ordered {
		if err := backend.Shutdown(ctx); err != nil {
			shutdownErr = errors.Join(shutdownErr, err)
			h.logger.DebugError(
				"shut down MCP backend resolver",
				err,
				"class",
				backend.Class(),
			)
		}
	}
	return shutdownErr
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
	if current.acquired == nil {
		if current.leases == 0 {
			delete(h.runtimes, current.resource.ID)
		}
		return
	}
	h.scheduleIdleLocked(current)
}

func (h *Service) scheduleIdleLocked(current *runtimeState) {
	if current == nil ||
		current.waiters != 0 ||
		current.streams != 0 ||
		current.starting != nil ||
		current.acquired == nil ||
		current.idle != nil {
		return
	}

	id := current.resource.ID
	timeout := h.idleTimeout(current.resource)
	current.idle = time.AfterFunc(timeout, func() {
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
		current.acquired == nil {
		h.mu.Unlock()
		return
	}
	acquired := current.acquired
	current.acquired = nil
	current.idle = nil
	current.lastErr = nil
	current.failures = 0
	current.retryAt = time.Time{}
	if current.leases == 0 {
		delete(h.runtimes, id)
	}
	h.mu.Unlock()

	if acquired != nil {
		acquired.Revoke()
		h.releaseBackend(acquired)
	}
}

func (h *Service) idleTimeout(
	resource resourcelease.Resolved,
) time.Duration {
	configuration, ok := resource.Configuration.(mcpresource.Config)
	if ok &&
		configuration.Server != nil &&
		configuration.Server.IdleTimeout > 0 {
		return configuration.Server.IdleTimeout
	}

	return h.options.IdleTimeout
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
	acquired mcpgateway.AcquiredBackend,
) {
	ctx, cancel := context.WithTimeout(
		context.Background(),
		h.options.CleanupTimeout,
	)
	defer cancel()
	if err := acquired.Release(ctx); err != nil {
		h.logger.DebugError("release MCP backend", err)
	}
}
