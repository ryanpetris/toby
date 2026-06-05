package sessionconfig

// Holder carries the resolved Config from the host-side resolver to the tools.
// It is a single shared instance: the resolver Sets it once per launch (during
// the context-files phase, after instructions are registered), and each tool
// Gets it when rendering. Access is synchronized because phase actions may run
// concurrently.

import "sync"

// Holder is a goroutine-safe carrier for the resolved Config.
type Holder struct {
	mu  sync.RWMutex
	cfg Config
}

// NewHolder returns an empty Holder.
func NewHolder() *Holder {
	return &Holder{}
}

// Set replaces the held Config. The resolver calls this once per launch.
func (h *Holder) Set(cfg Config) {
	h.mu.Lock()
	h.cfg = cfg
	h.mu.Unlock()
}

// Get returns the held Config. Tools call this when rendering.
func (h *Holder) Get() Config {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.cfg
}
