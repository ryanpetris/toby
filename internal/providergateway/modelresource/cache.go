package modelresource

// Coalesces and expires models discovery results by resource generation.

import (
	"context"
	"fmt"
	"time"

	"petris.dev/toby/internal/agent/protocol"
	"petris.dev/toby/internal/agent/resourcelease"
	configfile "petris.dev/toby/internal/config/file"
	modelsconfig "petris.dev/toby/internal/config/models"
)

type cacheState struct {
	models     map[string]any
	expires    time.Time
	generation uint64
	flight     *cacheFlight
	timer      *time.Timer
}

type cacheFlight struct {
	done       chan struct{}
	generation uint64
}

// ListModels performs one coalesced, cached discovery through the active
// models route.
func (h *Service) ListModels(
	ctx context.Context,
	request resourcelease.StreamRequest,
) (map[string]any, error) {
	if ctx == nil {
		return nil, fmt.Errorf("models list context is nil")
	}
	if _, ok := request.Resource.Configuration.(modelsconfig.Config); !ok {
		return nil, fmt.Errorf(
			"models resource has configuration type %T",
			request.Resource.Configuration,
		)
	}

	for {
		h.mu.Lock()
		if h.closing {
			h.mu.Unlock()
			return nil, fmt.Errorf(
				"models resource service is shutting down",
			)
		}
		current := h.runtimes[request.Resource.ID]
		if current == nil || current.leases == 0 {
			h.mu.Unlock()
			return nil, fmt.Errorf("models resource has no active lease")
		}
		cache := h.caches[request.Resource.ID]
		if cache == nil {
			cache = &cacheState{}
			h.caches[request.Resource.ID] = cache
		}
		if cache.models != nil && time.Now().Before(cache.expires) {
			models := configfile.CloneMap(cache.models)
			h.mu.Unlock()
			return models, nil
		}
		if cache.flight != nil {
			wait := cache.flight.done
			h.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-wait:
				continue
			}
		}

		flight := &cacheFlight{
			done:       make(chan struct{}),
			generation: cache.generation,
		}
		cache.flight = flight
		h.mu.Unlock()

		models, err := h.discover(ctx, request)

		h.mu.Lock()
		cache = h.caches[request.Resource.ID]
		if cache != nil && cache.flight == flight {
			cache.flight = nil
			if err == nil &&
				cache.generation == flight.generation &&
				!h.closing {
				cache.models = configfile.CloneMap(models)
				cache.expires = time.Now().Add(h.options.CacheTTL)
				h.scheduleCacheExpiryLocked(
					request.Resource.ID,
					cache,
				)
			}
			close(flight.done)
			if cache.models == nil && cache.flight == nil {
				delete(h.caches, request.Resource.ID)
			}
		}
		h.mu.Unlock()

		if err != nil {
			return nil, err
		}
		return configfile.CloneMap(models), nil
	}
}

// FlushModelsCache invalidates one resource generation without allowing an
// older in-flight discovery to repopulate it.
func (h *Service) FlushModelsCache(resource resourcelease.Resolved) {
	h.mu.Lock()
	defer h.mu.Unlock()

	cache := h.caches[resource.ID]
	if cache == nil {
		cache = &cacheState{}
		h.caches[resource.ID] = cache
	}
	cache.generation++
	cache.models = nil
	cache.expires = time.Time{}
	if cache.timer != nil {
		cache.timer.Stop()
		cache.timer = nil
	}
	if cache.flight == nil {
		delete(h.caches, resource.ID)
	}
}

func (h *Service) discover(
	ctx context.Context,
	request resourcelease.StreamRequest,
) (result map[string]any, returnErr error) {
	opened, err := h.Open(ctx, request)
	if err != nil {
		return nil, err
	}
	stream, ok := opened.(*stream)
	if !ok {
		return nil, fmt.Errorf(
			"models service returned stream type %T",
			opened,
		)
	}
	defer func() {
		h.logger.DebugError(
			"close models discovery stream",
			stream.Close(),
		)
	}()

	return stream.backend.Discover(ctx)
}

func (h *Service) scheduleCacheExpiryLocked(
	id protocol.ResourceID,
	expected *cacheState,
) {
	if expected.timer != nil {
		expected.timer.Stop()
	}
	generation := expected.generation
	expected.timer = time.AfterFunc(h.options.CacheTTL, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		current := h.caches[id]
		if current != expected ||
			current.generation != generation ||
			current.flight != nil {
			return
		}
		delete(h.caches, id)
	})
}
