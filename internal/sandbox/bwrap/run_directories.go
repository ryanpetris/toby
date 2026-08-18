package bwrap

// Retains the exact directories and lifetime lock for one published
// Bubblewrap run overlay.

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/storage/safefs"
)

// RunDirectories owns one unique run root and its sibling upper/work
// directories.
type RunDirectories struct {
	mu       sync.Mutex
	id       string
	parent   *safefs.Directory
	root     *safefs.Directory
	upper    *safefs.Directory
	work     *safefs.Directory
	runtime  *safefs.Directory
	lifetime *safefs.Lock
	limits   RunStorageLimits
	logger   *diagnostic.Logger
	closed   bool
}

var _ io.Closer = (*RunDirectories)(nil)

func openRunDirectories(
	parent *safefs.Directory,
	id string,
	limits RunStorageLimits,
	lifetime *safefs.Lock,
	logger *diagnostic.Logger,
) (*RunDirectories, error) {
	root, err := parent.OpenDirectory(id)
	if err != nil {
		return nil, err
	}
	upper, err := root.OpenDirectory("upper")
	if err != nil {
		logger.DebugError(
			"close Bubblewrap run root after upper directory open failed",
			root.Close(),
		)
		return nil, err
	}
	work, err := root.OpenDirectory("work")
	if err != nil {
		logger.DebugError(
			"close Bubblewrap run upper directory after work open failed",
			upper.Close(),
		)
		logger.DebugError(
			"close Bubblewrap run root after work directory open failed",
			root.Close(),
		)
		return nil, err
	}
	runtime, err := root.OpenDirectory("runtime")
	if err != nil {
		logger.DebugError(
			"close Bubblewrap run work directory after runtime open failed",
			work.Close(),
		)
		logger.DebugError(
			"close Bubblewrap run upper directory after runtime open failed",
			upper.Close(),
		)
		logger.DebugError(
			"close Bubblewrap run root after runtime directory open failed",
			root.Close(),
		)
		return nil, err
	}

	return &RunDirectories{
		id:       id,
		parent:   parent,
		root:     root,
		upper:    upper,
		work:     work,
		runtime:  runtime,
		lifetime: lifetime,
		limits:   limits,
		logger:   logger,
	}, nil
}

// RuntimePath returns the diagnostic host path of the private sidecar runtime
// directory. Foreground runs leave this sibling unused.
func (r *RunDirectories) RuntimePath() string {
	if r == nil {
		return ""
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.runtime == nil {
		return ""
	}

	return r.runtime.Path()
}

// RuntimeFile returns a caller-owned descriptor for the private sidecar
// runtime directory.
func (r *RunDirectories) RuntimeFile() (*os.File, error) {
	if r == nil {
		return nil, os.ErrInvalid
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.runtime == nil {
		return nil, os.ErrInvalid
	}

	return r.runtime.File()
}

// ID returns the random run identity used in deterministic plans.
func (r *RunDirectories) ID() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.id
}

// Overlay returns the path description for this retained upper/work pair.
func (r *RunDirectories) Overlay() Overlay {
	if r == nil {
		return Overlay{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.parent == nil || r.upper == nil || r.work == nil {
		return Overlay{}
	}

	return Overlay{
		RunStorageDir: r.parent.Path(),
		Upper:         r.upper.Path(),
		Work:          r.work.Path(),
	}
}

// UpperFile returns a caller-owned descriptor for the exact upper directory.
func (r *RunDirectories) UpperFile() (*os.File, error) {
	if r == nil {
		return nil, os.ErrInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.upper == nil {
		return nil, os.ErrInvalid
	}

	return r.upper.File()
}

// WorkFile returns a caller-owned descriptor for the exact work directory.
func (r *RunDirectories) WorkFile() (*os.File, error) {
	if r == nil {
		return nil, os.ErrInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.work == nil {
		return nil, os.ErrInvalid
	}

	return r.work.File()
}

// ReplaceOverlayFile creates any missing parent directories in the writable
// root overlay and atomically replaces the final entry with a regular file.
func (r *RunDirectories) ReplaceOverlayFile(
	name string,
	data []byte,
	mode fs.FileMode,
) error {
	if r == nil {
		return os.ErrInvalid
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.upper == nil {
		return os.ErrInvalid
	}

	parentName := filepath.Dir(name)
	parent := r.upper
	if parentName != "." {
		opened, err := r.upper.MkdirAll(parentName)
		if err != nil {
			return fmt.Errorf(
				"create overlay file parent %q: %w",
				parentName,
				err,
			)
		}
		defer func() {
			r.logger.DebugError(
				"close overlay file parent",
				opened.Close(),
				"path", parentName,
			)
		}()
		parent = opened
	}

	if err := parent.ReplaceFile(
		filepath.Base(name),
		data,
		mode,
	); err != nil {
		return fmt.Errorf("replace overlay file %q: %w", name, err)
	}

	return nil
}

// Close removes the exact transient run tree while its lifetime lock remains
// held. Cleanup uses repeated bounded passes; any remaining tree is left for
// the next run-storage recovery pass.
func (r *RunDirectories) Close() error {
	if r == nil {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}

	r.logger.DebugError(
		"close Bubblewrap runtime directory",
		closeDirectory(&r.runtime),
	)
	r.logger.DebugError(
		"close Bubblewrap work directory",
		closeDirectory(&r.work),
	)
	r.logger.DebugError(
		"close Bubblewrap upper directory",
		closeDirectory(&r.upper),
	)
	r.logger.DebugError(
		"close Bubblewrap run root",
		closeDirectory(&r.root),
	)

	for {
		mutation, err := r.parent.Lock(
			runMutationLockName,
			safefs.LockExclusive,
			false,
		)
		if err != nil {
			r.logger.DebugError(
				"lock Bubblewrap run storage for cleanup",
				err,
				"run_id", r.id,
			)
			break
		}
		removed, removeErr := r.parent.RemoveAllProgress(
			r.id,
			r.limits.MaxCleanupEntries,
		)
		r.logger.DebugError(
			"release Bubblewrap run-storage mutation lock",
			mutation.Close(),
		)
		if removeErr == nil {
			break
		}
		if !containsOnlyError(removeErr, safefs.ErrLimitExceeded) {
			r.logger.DebugError(
				"remove Bubblewrap run storage",
				removeErr,
				"run_id", r.id,
			)
			break
		}
		if removed == 0 {
			r.logger.DebugError(
				"remove Bubblewrap run storage",
				fmt.Errorf(
					"cleanup made no progress: %w",
					removeErr,
				),
				"run_id", r.id,
			)
			break
		}
	}

	r.closed = true
	r.logger.DebugError(
		"release Bubblewrap run lifetime lock",
		closeLock(r.lifetime),
		"run_id", r.id,
	)
	r.logger.DebugError(
		"close Bubblewrap run-storage root",
		r.parent.Close(),
		"run_id", r.id,
	)
	r.lifetime = nil
	r.parent = nil

	return nil
}

func containsOnlyError(err, target error) bool {
	if err == nil || target == nil {
		return false
	}
	if err == target {
		return true
	}

	if multiple, ok := err.(interface{ Unwrap() []error }); ok {
		children := multiple.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !containsOnlyError(child, target) {
				return false
			}
		}
		return true
	}
	if single, ok := err.(interface{ Unwrap() error }); ok {
		return containsOnlyError(single.Unwrap(), target)
	}

	return false
}

func closeDirectory(directory **safefs.Directory) error {
	if directory == nil || *directory == nil {
		return nil
	}
	err := (*directory).Close()
	*directory = nil

	return err
}
