package appconfig

// Carries the effective per-launch host configuration to statically composed
// process services without introducing a nested dependency-injection graph.

import "sync"

// LaunchHolder is the process-local, goroutine-safe carrier for the effective
// host configuration. It starts with the base configuration and is replaced
// once after launch overrides are resolved.
type LaunchHolder struct {
	mu      sync.RWMutex
	current *Service
}

// NewLaunchHolder initializes a holder with the process-wide base
// configuration.
func NewLaunchHolder(base *Service) *LaunchHolder {
	return &LaunchHolder{current: base}
}

// SetCurrent replaces the configuration exposed to launch-time consumers.
func (h *LaunchHolder) SetCurrent(current *Service) {
	if h == nil || current == nil {
		return
	}

	h.mu.Lock()
	h.current = current
	h.mu.Unlock()
}

// Current returns the current effective configuration.
func (h *LaunchHolder) Current() *Service {
	if h == nil {
		return nil
	}

	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.current
}
