//go:build linux

package socket

// Accepts peers and distinguishes Toby-owned from inherited sockets.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/diagnostic"
)

// Listener is a private Unix listener. Close removes the socket only while its
// original filesystem generation remains installed.
type Listener struct {
	raw        *net.UnixListener
	directory  *endpointDirectory
	generation socketGeneration
	address    *net.UnixAddr
	elected    bool

	acceptMu   sync.Mutex
	endpointMu sync.Mutex
	closed     bool
	closeOnce  sync.Once
	closeErr   error
}

var _ net.Listener = (*Listener)(nil)

// Generation returns the filesystem device and inode secured when the listener
// was created.
func (l *Listener) Generation() (uint64, uint64) {
	if l == nil {
		return 0, 0
	}

	return l.generation.device, l.generation.inode
}

// File returns a caller-owned O_PATH descriptor for the exact installed socket
// generation. It never follows a replacement symlink or reopens a different
// inode under the same pathname.
func (l *Listener) File() (*os.File, error) {
	if l == nil {
		return nil, net.ErrClosed
	}

	l.endpointMu.Lock()
	defer l.endpointMu.Unlock()

	if l.closed || l.directory == nil {
		return nil, net.ErrClosed
	}

	descriptor, err := unix.Openat(
		l.directory.fd,
		l.directory.name,
		unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"retain private socket generation: %w",
			err,
		)
	}
	file := os.NewFile(
		uintptr(descriptor),
		"private socket generation",
	)
	if file == nil {
		l.directory.logger.DebugError(
			"close invalid retained private socket descriptor",
			unix.Close(descriptor),
		)
		return nil, fmt.Errorf(
			"retain private socket generation: invalid descriptor",
		)
	}

	var status unix.Stat_t
	if err := unix.Fstat(descriptor, &status); err != nil {
		l.directory.logger.DebugError(
			"close retained private socket after inspection failure",
			file.Close(),
		)
		return nil, fmt.Errorf(
			"inspect retained private socket generation: %w",
			err,
		)
	}
	if status.Mode&unix.S_IFMT != unix.S_IFSOCK ||
		uint64(status.Dev) != l.generation.device ||
		status.Ino != l.generation.inode {
		l.directory.logger.DebugError(
			"close changed private socket generation",
			file.Close(),
		)
		return nil, fmt.Errorf(
			"%w: private socket changed before retention",
			ErrUnsafePath,
		)
	}

	return file, nil
}

// Accept waits for one peer. Close cancels a pending Accept.
func (l *Listener) Accept() (net.Conn, error) {
	if l == nil || l.raw == nil {
		return nil, net.ErrClosed
	}

	l.acceptMu.Lock()
	defer l.acceptMu.Unlock()

	return l.raw.AcceptUnix()
}

// Close stops accepting peers and removes this listener's socket generation.
func (l *Listener) Close() error {
	if l == nil {
		return nil
	}

	l.closeOnce.Do(func() {
		l.endpointMu.Lock()
		l.closed = true
		defer l.endpointMu.Unlock()

		if l.directory == nil {
			l.closeErr = l.raw.Close()
			return
		}

		if !l.elected {
			l.closeErr = errors.Join(
				l.raw.Close(),
				l.directory.removeGeneration(l.generation),
				l.directory.close(),
			)
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), operationLimit)
		defer cancel()

		lock, lockErr := l.directory.lock(ctx)
		if lockErr != nil {
			l.closeErr = errors.Join(
				l.raw.Close(),
				fmt.Errorf("lock private socket cleanup: %w", lockErr),
				l.directory.close(),
			)
			return
		}

		rawErr := l.raw.Close()
		removeErr := l.directory.removeGeneration(l.generation)
		lockCloseErr := lock.close()
		directoryCloseErr := l.directory.close()
		l.closeErr = errors.Join(
			rawErr,
			removeErr,
			lockCloseErr,
			directoryCloseErr,
		)
	})

	var logger *diagnostic.Logger
	if l.directory != nil {
		logger = l.directory.logger
	}
	logger.DebugError("close private socket listener", l.closeErr)
	return nil
}

// Addr returns the caller-supplied pathname rather than the internal
// descriptor-relative bind path.
func (l *Listener) Addr() net.Addr {
	if l == nil {
		return nil
	}

	return l.address
}
