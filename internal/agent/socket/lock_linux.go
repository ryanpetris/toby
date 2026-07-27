//go:build linux

package socket

// Serializes endpoint publication, private connection, and
// generation-safe cleanup without locking any application or private home.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/diagnostic"
)

const (
	operationLimit    = 5 * time.Second
	lockRetryInterval = 5 * time.Millisecond
)

type electionLock struct {
	fd     int
	path   string
	logger *diagnostic.Logger
}

func (d *endpointDirectory) lock(ctx context.Context) (*electionLock, error) {
	fd, _, err := d.openLockFile()
	if err != nil {
		return nil, err
	}

	if err := d.validateLockFile(fd, d.lockName); err != nil {
		closeDescriptor(d.logger, fd)
		return nil, err
	}

	bounded, cancel, err := boundedContext(ctx)
	if err != nil {
		closeDescriptor(d.logger, fd)
		return nil, err
	}
	defer cancel()

	timer := time.NewTimer(0)
	defer timer.Stop()

	for {
		select {
		case <-bounded.Done():
			closeDescriptor(d.logger, fd)
			return nil, bounded.Err()
		case <-timer.C:
		}

		err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return &electionLock{
				fd:     fd,
				path:   d.lockName,
				logger: d.logger,
			}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			closeDescriptor(d.logger, fd)
			return nil, &fs.PathError{Op: "lock agent election", Path: d.lockName, Err: err}
		}

		timer.Reset(lockRetryInterval)
	}
}

func (d *endpointDirectory) openLockFile() (int, bool, error) {
	fd, err := unix.Openat(
		d.fd,
		d.lockName,
		unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		privateFileMode,
	)
	if err == nil {
		return fd, true, nil
	}
	if !errors.Is(err, unix.EEXIST) {
		return -1, false, unsafePathError("create agent election lock", d.lockName, err)
	}

	fd, err = unix.Openat(
		d.fd,
		d.lockName,
		unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return -1, false, unsafePathError("open agent election lock", d.lockName, err)
	}

	return fd, false, nil
}

func (d *endpointDirectory) validateLockFile(fd int, path string) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return &fs.PathError{Op: "inspect agent election lock", Path: path, Err: err}
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf("%w: agent election lock %q is not a regular file", ErrUnsafePath, path)
	}
	if int(stat.Uid) != int(d.uid) || int(stat.Gid) != int(d.gid) {
		if err := unix.Fchown(fd, int(d.uid), int(d.gid)); err != nil {
			d.logger.DebugError(
				"correct agent election lock ownership",
				err,
				"path", path,
				"current_uid", stat.Uid,
				"current_gid", stat.Gid,
				"desired_uid", d.uid,
				"desired_gid", d.gid,
			)
		}
	}
	if stat.Mode&0o777 != privateFileMode {
		if err := unix.Fchmod(fd, privateFileMode); err != nil {
			d.logger.DebugError(
				"correct agent election lock mode",
				err,
				"path", path,
				"current_mode", fmt.Sprintf("%#o", stat.Mode&0o777),
				"desired_mode", "0600",
			)
		}
	}

	return nil
}

func (l *electionLock) close() error {
	if l == nil || l.fd < 0 {
		return nil
	}

	unlockErr := unix.Flock(l.fd, unix.LOCK_UN)
	closeErr := unix.Close(l.fd)
	l.fd = -1

	err := errors.Join(
		pathError("unlock agent election", l.path, unlockErr),
		pathError("close agent election lock", l.path, closeErr),
	)
	l.logger.DebugError("release agent election lock", err)
	return nil
}

func boundedContext(parent context.Context) (context.Context, context.CancelFunc, error) {
	if parent == nil {
		return nil, nil, fmt.Errorf("private socket context must not be nil")
	}
	if err := parent.Err(); err != nil {
		return nil, nil, err
	}

	limit := time.Now().Add(operationLimit)
	if deadline, ok := parent.Deadline(); ok && !deadline.After(limit) {
		context, cancel := context.WithCancel(parent)
		return context, cancel, nil
	}

	context, cancel := context.WithDeadline(parent, limit)
	return context, cancel, nil
}

func pathError(operation string, path string, err error) error {
	if err == nil {
		return nil
	}

	return &fs.PathError{Op: operation, Path: path, Err: err}
}
