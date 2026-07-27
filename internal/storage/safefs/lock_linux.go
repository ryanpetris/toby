//go:build linux

package safefs

// Acquires shared and exclusive advisory locks on safe files and directories.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/diagnostic"
)

// Lock opens or creates a regular lock file and acquires an advisory flock.
// Newly created lock files request mode 0600. A non-blocking conflict returns
// ErrWouldBlock.
func (d *Directory) Lock(name string, mode LockMode, nonBlocking bool) (*Lock, error) {
	operation, err := flockOperation(mode)
	if err != nil {
		return nil, err
	}
	if nonBlocking {
		operation |= unix.LOCK_NB
	}

	parentFD, base, path, err := d.openParent(name)
	if err != nil {
		return nil, err
	}
	defer closeDescriptor(d.logger, parentFD)

	fd, created, err := openLockFile(
		parentFD,
		base,
		path,
		d.logger,
	)
	if err != nil {
		return nil, err
	}
	if created {
		if err := unix.Fsync(parentFD); err != nil {
			closeDescriptor(d.logger, fd)
			return nil, &fs.PathError{Op: "sync lock directory", Path: filepath.Dir(path), Err: err}
		}
	}

	if err := unix.Flock(fd, operation); err != nil {
		closeDescriptor(d.logger, fd)
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, fmt.Errorf("%w: %q", ErrWouldBlock, path)
		}
		return nil, &fs.PathError{Op: "lock file", Path: path, Err: err}
	}

	return &Lock{
		file:   os.NewFile(uintptr(fd), path),
		logger: d.logger,
	}, nil
}

// LockSelf acquires an advisory flock on the retained directory through an
// independent open file description. Closing the returned lock releases it
// without closing the directory capability.
func (d *Directory) LockSelf(mode LockMode, nonBlocking bool) (*Lock, error) {
	operation, err := flockOperation(mode)
	if err != nil {
		return nil, err
	}
	if nonBlocking {
		operation |= unix.LOCK_NB
	}

	fd, err := d.openIndependent()
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(fd, operation); err != nil {
		closeDescriptor(d.logger, fd)
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, fmt.Errorf("%w: %q", ErrWouldBlock, d.Path())
		}
		return nil, &fs.PathError{Op: "lock directory", Path: d.Path(), Err: err}
	}

	return &Lock{
		file:   os.NewFile(uintptr(fd), d.Path()),
		logger: d.logger,
	}, nil
}

// Close releases the flock and closes its open file description.
func (l *Lock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}

	file := l.file
	l.file = nil

	raw, err := file.SyscallConn()
	if err != nil {
		l.logger.DebugError("access filesystem lock for release", err)
		l.logger.DebugError("close inaccessible filesystem lock", file.Close())
		return nil
	}

	var unlockErr error
	if err := raw.Control(func(fd uintptr) {
		unlockErr = unix.Flock(int(fd), unix.LOCK_UN)
	}); err != nil {
		l.logger.DebugError("access filesystem lock descriptor for release", err)
		l.logger.DebugError("close inaccessible filesystem lock", file.Close())
		return nil
	}
	l.logger.DebugError("unlock filesystem lock", unlockErr)
	l.logger.DebugError("close filesystem lock", file.Close())
	return nil
}

func flockOperation(mode LockMode) (int, error) {
	switch mode {
	case LockShared:
		return unix.LOCK_SH, nil
	case LockExclusive:
		return unix.LOCK_EX, nil
	default:
		return 0, fmt.Errorf("invalid lock mode %d", mode)
	}
}

func openLockFile(
	parentFD int,
	base string,
	path string,
	logger *diagnostic.Logger,
) (int, bool, error) {
	for range regularOpenAttempts {
		fd, err := openExistingRegular(
			parentFD,
			base,
			unix.O_RDWR,
			path,
			logger,
		)
		if err == nil {
			return fd, false, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return -1, false, err
		}

		fd, err = openRelative(
			parentFD,
			base,
			unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NONBLOCK,
			0o600,
			logger,
		)
		if err == nil {
			var stat unix.Stat_t
			if err := unix.Fstat(fd, &stat); err != nil {
				closeDescriptor(logger, fd)
				logger.DebugError(
					"remove uninspected created lock file",
					removeCreatedFile(parentFD, base, path),
					"path", path,
				)
				return -1, false, &fs.PathError{
					Op:   "stat lock file",
					Path: path,
					Err:  err,
				}
			}
			if err := validateRegularStat(&stat, path); err != nil {
				closeDescriptor(logger, fd)
				logger.DebugError(
					"remove invalid created lock file",
					removeCreatedFile(parentFD, base, path),
					"path", path,
				)
				return -1, false, err
			}
			return fd, true, nil
		}
		if !errors.Is(err, unix.EEXIST) {
			return -1, false, &fs.PathError{Op: "create lock file", Path: path, Err: err}
		}
	}

	return -1, false, fmt.Errorf("%w: lock file %q changed while it was created", ErrUnsafePath, path)
}
