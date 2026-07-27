package sessionconfig

// Holder carries the resolved Config from the host-side resolver to the tools.
// It is a single shared instance: the launch resolver Sets it once after
// acquiring run-scoped services, and each tool reads it when rendering. Access
// is synchronized because phase actions may run concurrently.

import (
	"fmt"
	"sync"
)

// Holder is a goroutine-safe carrier for the resolved Config.
type Holder struct {
	mu       sync.RWMutex
	cfg      Config
	resolved bool
}

// NewHolder returns an empty Holder.
func NewHolder() *Holder {
	return &Holder{}
}

// Set replaces the held Config. The resolver calls this once per launch.
func (h *Holder) Set(cfg Config) {
	h.mu.Lock()
	h.cfg = cfg.Clone()
	h.resolved = true
	h.mu.Unlock()
}

// Snapshot returns a copy of the currently held Config.
func (h *Holder) Snapshot() Config {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.cfg.Clone()
}

// Config returns the resolved configuration or fails when the launch has not
// yet crossed its session-resolution boundary.
func (h *Holder) Config() (Config, error) {
	if h == nil {
		return Config{}, fmt.Errorf("session configuration holder is nil")
	}

	h.mu.RLock()
	defer h.mu.RUnlock()
	if !h.resolved {
		return Config{}, fmt.Errorf("session configuration is not resolved")
	}

	return h.cfg.Clone(), nil
}
