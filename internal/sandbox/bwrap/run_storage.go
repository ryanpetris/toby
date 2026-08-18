package bwrap

// Creates unique owner-only Bubblewrap upper/work directory pairs and retains
// their lifetime locks until exact no-follow teardown.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/storage/recovery"
	"petris.dev/toby/internal/storage/safefs"
)

const (
	runMutationLockName       = ".mutation.lock"
	runLifetimeLockName       = ".lock"
	runPublicationTemporary   = ".toby-tmp-"
	defaultRunCleanupEntries  = 2_000_000
	defaultRunRecoveryEntries = 4096
	runIDRandomBytes          = 16
	runCreateAttempts         = 16
)

var generatedRunIDPattern = regexp.MustCompile(`^run-[0-9a-f]{32}$`)

// RunStorageLimits bound one startup recovery pass and one recursive teardown.
type RunStorageLimits struct {
	MaxRecoveryCandidates int
	MaxCleanupEntries     uint64
}

// DefaultRunStorageLimits returns production bounds for transient run state.
func DefaultRunStorageLimits() RunStorageLimits {
	return RunStorageLimits{
		MaxRecoveryCandidates: defaultRunRecoveryEntries,
		MaxCleanupEntries:     defaultRunCleanupEntries,
	}
}

// RunStorage owns the transient per-user directory beneath which Bubblewrap
// run overlays are created.
type RunStorage struct {
	mu     sync.Mutex
	root   *safefs.Directory
	limits RunStorageLimits
	logger *diagnostic.Logger
	closed bool
}

var _ io.Closer = (*RunStorage)(nil)

// OpenRunStorage opens or creates a run-storage root and recovers abandoned
// runs.
func OpenRunStorage(
	path string,
	limits RunStorageLimits,
	logger *diagnostic.Logger,
) (*RunStorage, error) {
	limits, err := normalizeRunStorageLimits(limits)
	if err != nil {
		return nil, err
	}

	root, err := openRunStorageRoot(path, logger)
	if err != nil {
		return nil, fmt.Errorf("open Bubblewrap run storage: %w", err)
	}
	mutation, err := root.Lock(
		runMutationLockName,
		safefs.LockExclusive,
		false,
	)
	if err != nil {
		logger.DebugError(
			"close Bubblewrap run storage after mutation lock failure",
			root.Close(),
		)
		return nil, err
	}
	recoveryErr := recovery.CleanupTemporaryDirectories(
		root,
		uint64(limits.MaxRecoveryCandidates),
		limits.MaxCleanupEntries,
	)
	if recoveryErr == nil {
		recoveryErr = recoverPublishedRuns(root, limits, logger)
	}
	logger.DebugError("recover Bubblewrap run storage", recoveryErr)
	logger.DebugError(
		"release Bubblewrap run-storage mutation lock",
		mutation.Close(),
	)

	return &RunStorage{
		root:   root,
		limits: limits,
		logger: logger,
	}, nil
}

func openRunStorageRoot(
	path string,
	logger *diagnostic.Logger,
) (*safefs.Directory, error) {
	if path == "" ||
		strings.IndexByte(path, 0) >= 0 ||
		!filepath.IsAbs(path) ||
		filepath.Clean(path) != path {
		return nil, fmt.Errorf(
			"%w: run-storage path %q must be clean and absolute",
			safefs.ErrUnsafePath,
			path,
		)
	}

	cacheRoot, err := safefs.OpenOrCreateRoot(
		filepath.Dir(path),
		safefs.DirectoryOptions{
			OwnerUID: os.Geteuid(),
			OwnerGID: os.Getegid(),
			Logger:   logger,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("open Toby cache root: %w", err)
	}
	root, err := cacheRoot.MkdirAll(filepath.Base(path))
	if err != nil {
		logger.DebugError(
			"close Toby cache root after run-storage setup failure",
			cacheRoot.Close(),
		)
		return nil, fmt.Errorf("open run-storage directory: %w", err)
	}
	root.RepairPrivateOwnershipAndMode()
	logger.DebugError("close Toby cache root", cacheRoot.Close())

	return root, nil
}

// Create publishes one unique sibling upper/work pair and holds its lifetime
// lock before the final run name becomes visible.
func (s *RunStorage) Create(
	ctx context.Context,
) (*RunDirectories, error) {
	if ctx == nil {
		return nil, fmt.Errorf("create Bubblewrap run storage: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.Lock()
	if s.closed || s.root == nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("bubblewrap run storage is closed")
	}
	root, err := s.root.Duplicate()
	limits := s.limits
	s.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("duplicate Bubblewrap run storage: %w", err)
	}

	mutation, err := root.Lock(
		runMutationLockName,
		safefs.LockExclusive,
		false,
	)
	if err != nil {
		s.logger.DebugError(
			"close Bubblewrap run-storage root after mutation lock failure",
			root.Close(),
		)
		return nil, err
	}
	defer func() {
		s.logger.DebugError(
			"release Bubblewrap run-storage mutation lock",
			mutation.Close(),
		)
	}()

	for range runCreateAttempts {
		if err := ctx.Err(); err != nil {
			s.logger.DebugError(
				"close Bubblewrap run-storage root after cancellation",
				root.Close(),
			)
			return nil, err
		}
		id, err := newRunID()
		if err != nil {
			s.logger.DebugError(
				"close Bubblewrap run-storage root after ID generation failure",
				root.Close(),
			)
			return nil, err
		}

		var lifetime *safefs.Lock
		published, err := root.PublishDirectory(
			id,
			limits.MaxCleanupEntries,
			func(stage *safefs.Directory) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				upper, err := stage.MkdirAll("upper")
				if err != nil {
					return err
				}
				work, err := stage.MkdirAll("work")
				if err != nil {
					s.logger.DebugError(
						"close Bubblewrap run upper directory",
						upper.Close(),
					)
					return err
				}
				runtime, err := stage.MkdirAll("runtime")
				if err != nil {
					s.logger.DebugError(
						"close Bubblewrap run work directory",
						work.Close(),
					)
					s.logger.DebugError(
						"close Bubblewrap run upper directory",
						upper.Close(),
					)
					return err
				}
				s.logger.DebugError(
					"close Bubblewrap run runtime directory",
					runtime.Close(),
				)
				s.logger.DebugError(
					"close Bubblewrap run work directory",
					work.Close(),
				)
				s.logger.DebugError(
					"close Bubblewrap run upper directory",
					upper.Close(),
				)

				lifetime, err = stage.Lock(
					runLifetimeLockName,
					safefs.LockExclusive,
					false,
				)
				if err != nil {
					return err
				}
				return ctx.Err()
			},
		)
		if err != nil {
			s.logger.DebugError(
				"close unpublished Bubblewrap run lifetime lock",
				closeLock(lifetime),
			)
			s.logger.DebugError(
				"close Bubblewrap run-storage root after publication failure",
				root.Close(),
			)
			return nil, fmt.Errorf(
				"publish Bubblewrap run %q: %w",
				id,
				err,
			)
		}
		if !published {
			s.logger.DebugError(
				"close unpublished Bubblewrap run lifetime lock",
				closeLock(lifetime),
			)
			continue
		}
		if err := ctx.Err(); err != nil {
			s.logger.DebugError(
				"remove cancelled Bubblewrap run",
				root.RemoveAll(id, limits.MaxCleanupEntries),
			)
			s.logger.DebugError(
				"close cancelled Bubblewrap run lifetime lock",
				closeLock(lifetime),
			)
			s.logger.DebugError(
				"close Bubblewrap run-storage root after cancellation",
				root.Close(),
			)
			return nil, err
		}

		run, err := openRunDirectories(
			root,
			id,
			limits,
			lifetime,
			s.logger,
		)
		if err != nil {
			s.logger.DebugError(
				"remove unopened Bubblewrap run",
				root.RemoveAll(id, limits.MaxCleanupEntries),
			)
			s.logger.DebugError(
				"close unopened Bubblewrap run lifetime lock",
				closeLock(lifetime),
			)
			s.logger.DebugError(
				"close Bubblewrap run-storage root after run open failure",
				root.Close(),
			)
			return nil, err
		}

		return run, nil
	}

	s.logger.DebugError(
		"close Bubblewrap run-storage root after allocation failure",
		root.Close(),
	)
	return nil, fmt.Errorf("could not allocate a unique Bubblewrap run ID")
}

// RootFile returns a caller-owned descriptor for the complete per-user
// Bubblewrap run-storage root.
func (s *RunStorage) RootFile() (*os.File, error) {
	if s == nil {
		return nil, fmt.Errorf("bubblewrap run storage is nil")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.root == nil {
		return nil, fmt.Errorf("bubblewrap run storage is closed")
	}

	return s.root.File()
}

// Close releases the run-storage root capability. Existing RunDirectories
// retain their own parent capability and remain independently closable.
func (s *RunStorage) Close() error {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	root := s.root
	s.root = nil
	s.mu.Unlock()

	return root.Close()
}

func normalizeRunStorageLimits(limits RunStorageLimits) (RunStorageLimits, error) {
	defaults := DefaultRunStorageLimits()
	if limits.MaxRecoveryCandidates == 0 {
		limits.MaxRecoveryCandidates = defaults.MaxRecoveryCandidates
	}
	if limits.MaxCleanupEntries == 0 {
		limits.MaxCleanupEntries = defaults.MaxCleanupEntries
	}
	if limits.MaxRecoveryCandidates < 0 {
		return RunStorageLimits{}, fmt.Errorf("run-storage limits must be positive")
	}

	return limits, nil
}

func newRunID() (string, error) {
	var random [runIDRandomBytes]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate Bubblewrap run ID: %w", err)
	}
	return "run-" + hex.EncodeToString(random[:]), nil
}

func closeLock(lock *safefs.Lock) error {
	if lock == nil {
		return nil
	}
	return lock.Close()
}
