package resource

// A Lease is one project's hold on a shared backend. It exposes what the project
// registers on its own proxy — a streamable-HTTP handler (stdio) or an upstream URL +
// headers (http/remote) — plus the backend's status and key. Release drops the hold
// exactly once; the last release stops the backend.

import (
	"net/http"
	"sync"
)

type Lease struct {
	registry *Registry
	backend  *backend
	once     sync.Once
}

// Name is the config server name.
func (l *Lease) Name() string { return l.backend.name }

// Key identifies the shared backend (for Registry.Restart).
func (l *Lease) Key() string { return l.backend.key }

// Remote reports whether the backend is a direct upstream (no container).
func (l *Lease) Remote() bool { return l.backend.remote }

// Handler is the streamable-HTTP handler for a stdio backend, or nil.
func (l *Lease) Handler() http.Handler { return l.backend.handler() }

// Upstream is the URL + headers for an http/remote backend.
func (l *Lease) Upstream() (string, http.Header) { return l.backend.upstream() }

// Snapshot is the backend's current sanitized status.
func (l *Lease) Snapshot() StatusSnapshot { return l.backend.snapshot() }

// Release drops this project's hold; safe to call more than once.
func (l *Lease) Release() {
	l.once.Do(func() { l.registry.release(l.backend) })
}
