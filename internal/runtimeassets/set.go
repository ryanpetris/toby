package runtimeassets

// Exposes materialized Bubblewrap inputs and releases their run-scoped
// descriptors and storage.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/sandbox/bwrap"
	"petris.dev/toby/internal/storage/safefs"
)

// Set owns the exact source descriptors and private temporary storage for one
// materialization. It must remain open until the Bubblewrap run has retained
// its sources.
type Set struct {
	mu             sync.Mutex
	assets         []bwrap.RuntimeAsset
	sources        map[string]*os.File
	parent         *safefs.Directory
	storageName    string
	cleanupEntries uint64
	logger         *diagnostic.Logger
	sourcesClosed  bool
	closed         bool
}

var _ io.Closer = (*Set)(nil)

// RuntimeAssets returns detached, deterministic Bubblewrap metadata for the
// materialized files.
func (s *Set) RuntimeAssets() []bwrap.RuntimeAsset {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]bwrap.RuntimeAsset(nil), s.assets...)
}

// Sources returns a detached map containing Set-owned descriptors suitable for
// bwrap.Sources.RuntimeAssets. Callers must not close the descriptors; they
// remain valid until Set.Close.
func (s *Set) Sources() (map[string]*os.File, error) {
	if s == nil {
		return nil, os.ErrInvalid
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.sourcesClosed {
		return nil, fmt.Errorf("runtime assets are closed: %w", os.ErrClosed)
	}

	sources := make(map[string]*os.File, len(s.sources))
	for target, source := range s.sources {
		sources[target] = source
	}

	return sources, nil
}

// TransferStorageCleanup closes the materialization descriptors while leaving
// its files in place for an enclosing run-directory owner to remove. This is
// used only after Bubblewrap has retained every source descriptor: Bubblewrap
// resolves bind FDs through /proc during setup, so their backing names remain
// linked for the run-directory owner's lifetime.
func (s *Set) TransferStorageCleanup() error {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}

	s.logger.DebugError(
		"close runtime asset sources after cleanup transfer",
		s.closeSources(),
	)
	s.logger.DebugError(
		"close runtime asset root after cleanup transfer",
		closeDirectory(s.parent),
	)
	s.parent = nil
	s.storageName = ""
	s.closed = true

	return nil
}

// Close releases source descriptors and best-effort removes the
// materialization's private storage.
func (s *Set) Close() error {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}

	s.logger.DebugError(
		"close runtime asset sources",
		s.closeSources(),
	)

	if s.parent != nil && s.storageName != "" {
		s.logger.DebugError(
			"remove runtime asset storage",
			s.parent.RemoveAllOwned(
				s.storageName,
				s.cleanupEntries,
			),
			"storage_name", s.storageName,
		)
	}

	s.logger.DebugError(
		"close runtime asset root",
		closeDirectory(s.parent),
	)
	s.parent = nil
	s.storageName = ""
	s.closed = true

	return nil
}

func (s *Set) closeSources() error {
	if s.sourcesClosed {
		return nil
	}

	var closeErr error
	for target, source := range s.sources {
		if source != nil {
			closeErr = errors.Join(closeErr, source.Close())
		}
		delete(s.sources, target)
	}
	s.sourcesClosed = true

	return closeErr
}

func closeDirectory(directory *safefs.Directory) error {
	if directory == nil {
		return nil
	}

	return directory.Close()
}
