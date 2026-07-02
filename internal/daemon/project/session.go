// A Session is one client's hold on a project: it keeps the container alive (via the
// refcount) for as long as the client's tool runs, and exposes the project Handle the
// session layer drives. Release drops the hold exactly once.

package project

import "sync"

// Session is a refcounted hold on a live project.
type Session struct {
	registry *Registry
	entry    *entry
	once     sync.Once
}

// Handle returns the live project's runtime.
func (s *Session) Handle() Handle { return s.entry.handle }

// Key is the project's identity.
func (s *Session) Key() Key { return s.entry.key }

// Release drops this session's hold; safe to call more than once.
func (s *Session) Release() {
	s.once.Do(func() { s.registry.release(s.entry) })
}
