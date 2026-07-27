package tobymcp

// Defines the native per-connection execution context and serialization lock.

import (
	"sync"
)

// Session is the per-connection execution context shared across tool and
// resource calls. Snapshot is the sole introspection source. Git is a live
// launch-owned reverse capability and Resources is a detached service catalog.
type Session struct {
	Git       GitClient
	Snapshot  SessionSnapshot
	Resources []Resource
	mu        sync.Mutex
}

// Serialize runs fn while holding the session lock, so concurrent tool calls do
// not interleave host operations within one session.
func (s *Session) Serialize(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn()
}
