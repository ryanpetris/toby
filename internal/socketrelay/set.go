package socketrelay

// Exposes exact Bubblewrap inputs and closes every relay as one run-owned set.

import (
	"fmt"
	"io"
	"os"
	"sync"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/sandbox/bwrap"
)

// Set owns the listeners, active connections, and pinned descriptors for one
// run's socket relays.
type Set struct {
	mu sync.Mutex

	relays  []*relay
	assets  []bwrap.RuntimeAsset
	sources map[string]*os.File
	logger  *diagnostic.Logger
	closed  bool
}

var _ io.Closer = (*Set)(nil)

// RuntimeAssets returns detached Bubblewrap metadata for every relay endpoint.
func (s *Set) RuntimeAssets() []bwrap.RuntimeAsset {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}

	return append([]bwrap.RuntimeAsset(nil), s.assets...)
}

// Sources returns a detached map of Set-owned pinned endpoint descriptors.
// Callers must not close them.
func (s *Set) Sources() (map[string]*os.File, error) {
	if s == nil {
		return nil, fmt.Errorf("socket relay set is nil")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, fmt.Errorf("socket relays are closed: %w", os.ErrClosed)
	}

	sources := make(map[string]*os.File, len(s.sources))
	for target, source := range s.sources {
		sources[target] = source
	}

	return sources, nil
}

// Close revokes every endpoint, interrupts active streams, and releases all
// pinned endpoint descriptors.
func (s *Set) Close() error {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}

	for index := len(s.relays) - 1; index >= 0; index-- {
		s.logger.DebugError(
			"close socket relay",
			s.relays[index].Close(),
		)
		s.relays[index] = nil
	}
	s.relays = nil

	for target, source := range s.sources {
		if source != nil {
			s.logger.DebugError(
				"close socket relay source",
				source.Close(),
				"target",
				target,
			)
		}
		delete(s.sources, target)
	}
	s.assets = nil
	s.closed = true

	return nil
}
