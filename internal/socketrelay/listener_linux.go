//go:build linux

package socketrelay

// Creates and pins one run-scoped Unix listener beneath an exact directory
// descriptor.

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/storage/safefs"
)

const privateSocketMode = uint32(0o600)

type socketGeneration struct {
	device uint64
	inode  uint64
}

type privateListener struct {
	raw        *net.UnixListener
	root       *os.File
	source     *os.File
	name       string
	hostPath   string
	generation socketGeneration
	logger     *diagnostic.Logger

	closeOnce sync.Once
	closeErr  error
}

func listenPrivate(
	root *safefs.Directory,
	logger *diagnostic.Logger,
	name string,
) (*privateListener, error) {
	if root == nil {
		return nil, fmt.Errorf("socket relay runtime root is nil")
	}
	if name == "" ||
		filepath.Base(name) != name ||
		filepath.Clean(name) != name {
		return nil, fmt.Errorf("invalid socket relay filename %q", name)
	}

	rootFile, err := root.File()
	if err != nil {
		return nil, fmt.Errorf("retain socket relay runtime root: %w", err)
	}
	if err := validateRelayRoot(rootFile); err != nil {
		logger.DebugError(
			"close invalid socket relay runtime root",
			rootFile.Close(),
		)
		return nil, err
	}
	if exists, err := entryExists(rootFile, name); err != nil {
		logger.DebugError(
			"close socket relay runtime root after inspection failed",
			rootFile.Close(),
		)
		return nil, err
	} else if exists {
		err := fmt.Errorf(
			"socket relay endpoint %q already exists",
			name,
		)
		logger.DebugError(
			"close socket relay runtime root for existing endpoint",
			rootFile.Close(),
		)
		return nil, err
	}

	descriptorPath := fmt.Sprintf(
		"/proc/self/fd/%d/%s",
		rootFile.Fd(),
		name,
	)
	if len(descriptorPath) > len(unix.RawSockaddrUnix{}.Path)-1 {
		err := fmt.Errorf(
			"socket relay descriptor path is too long: %q",
			descriptorPath,
		)
		logger.DebugError(
			"close socket relay runtime root for invalid endpoint path",
			rootFile.Close(),
		)
		return nil, err
	}

	raw, err := net.ListenUnix(
		"unix",
		&net.UnixAddr{Name: descriptorPath, Net: "unix"},
	)
	if err != nil {
		listenErr := fmt.Errorf(
			"listen on socket relay endpoint: %w",
			err,
		)
		logger.DebugError(
			"close socket relay runtime root after listen failed",
			rootFile.Close(),
		)
		return nil, listenErr
	}
	raw.SetUnlinkOnClose(false)

	generation, err := inspectSocket(rootFile, name)
	if err != nil {
		return nil, closeUnpublishedListener(
			err,
			raw,
			rootFile,
			name,
			socketGeneration{},
			logger,
		)
	}
	if err := chmodSocket(rootFile, name); err != nil {
		logger.DebugError(
			"correct socket relay mode",
			err,
			"path",
			filepath.Join(root.Path(), name),
			"desired_mode",
			"0600",
		)
	}

	securedGeneration, err := inspectSocket(rootFile, name)
	if err != nil {
		return nil, closeUnpublishedListener(
			err,
			raw,
			rootFile,
			name,
			generation,
			logger,
		)
	}
	if securedGeneration != generation {
		return nil, closeUnpublishedListener(
			fmt.Errorf(
				"socket relay endpoint changed while it was secured",
			),
			raw,
			rootFile,
			name,
			generation,
			logger,
		)
	}
	source, err := openSocketSource(rootFile, name, generation, logger)
	if err != nil {
		return nil, closeUnpublishedListener(
			err,
			raw,
			rootFile,
			name,
			generation,
			logger,
		)
	}

	return &privateListener{
		raw:        raw,
		root:       rootFile,
		source:     source,
		name:       name,
		hostPath:   filepath.Join(root.Path(), name),
		generation: generation,
		logger:     logger,
	}, nil
}

func validateRelayRoot(
	root *os.File,
) error {
	var status unix.Stat_t
	if err := unix.Fstat(int(root.Fd()), &status); err != nil {
		return fmt.Errorf("inspect socket relay runtime root: %w", err)
	}
	if status.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("socket relay runtime root is not a directory")
	}

	return nil
}

func entryExists(root *os.File, name string) (bool, error) {
	var status unix.Stat_t
	err := unix.Fstatat(
		int(root.Fd()),
		name,
		&status,
		unix.AT_SYMLINK_NOFOLLOW,
	)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}

	return false, &fs.PathError{
		Op:   "inspect socket relay endpoint",
		Path: name,
		Err:  err,
	}
}

func inspectSocket(
	root *os.File,
	name string,
) (socketGeneration, error) {
	var status unix.Stat_t
	if err := unix.Fstatat(
		int(root.Fd()),
		name,
		&status,
		unix.AT_SYMLINK_NOFOLLOW,
	); err != nil {
		return socketGeneration{}, &fs.PathError{
			Op:   "inspect socket relay endpoint",
			Path: name,
			Err:  err,
		}
	}
	if status.Mode&unix.S_IFMT != unix.S_IFSOCK {
		return socketGeneration{}, fmt.Errorf(
			"socket relay endpoint %q is not a Unix socket",
			name,
		)
	}

	return socketGeneration{
		device: uint64(status.Dev),
		inode:  status.Ino,
	}, nil
}

func chmodSocket(root *os.File, name string) error {
	err := unix.Fchmodat(
		int(root.Fd()),
		name,
		privateSocketMode,
		unix.AT_SYMLINK_NOFOLLOW,
	)
	if !errors.Is(err, unix.EOPNOTSUPP) {
		return err
	}

	return unix.Fchmodat(
		int(root.Fd()),
		name,
		privateSocketMode,
		0,
	)
}

func openSocketSource(
	root *os.File,
	name string,
	generation socketGeneration,
	logger *diagnostic.Logger,
) (*os.File, error) {
	descriptor, err := unix.Openat(
		int(root.Fd()),
		name,
		unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("pin socket relay endpoint: %w", err)
	}

	source := os.NewFile(uintptr(descriptor), "socket relay endpoint")
	if source == nil {
		logger.DebugError(
			"close invalid socket relay endpoint descriptor",
			unix.Close(descriptor),
		)
		return nil, fmt.Errorf("pin socket relay endpoint: invalid descriptor")
	}

	var status unix.Stat_t
	if err := unix.Fstat(descriptor, &status); err != nil {
		logger.DebugError(
			"close uninspected socket relay endpoint",
			source.Close(),
		)
		return nil, fmt.Errorf(
			"inspect pinned socket relay endpoint: %w",
			err,
		)
	}
	if status.Mode&unix.S_IFMT != unix.S_IFSOCK ||
		uint64(status.Dev) != generation.device ||
		status.Ino != generation.inode {
		logger.DebugError(
			"close replaced socket relay endpoint",
			source.Close(),
		)
		return nil, fmt.Errorf(
			"socket relay endpoint changed before it was pinned",
		)
	}

	return source, nil
}

func (l *privateListener) File() (*os.File, error) {
	if l == nil || l.source == nil {
		return nil, net.ErrClosed
	}

	descriptor, err := unix.FcntlInt(
		l.source.Fd(),
		unix.F_DUPFD_CLOEXEC,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("duplicate socket relay endpoint: %w", err)
	}
	duplicate := os.NewFile(
		uintptr(descriptor),
		"socket relay endpoint duplicate",
	)
	if duplicate == nil {
		l.logger.DebugError(
			"close invalid socket relay endpoint duplicate",
			unix.Close(descriptor),
		)
		return nil, fmt.Errorf(
			"duplicate socket relay endpoint: invalid descriptor",
		)
	}

	return duplicate, nil
}

func (l *privateListener) Accept() (*net.UnixConn, error) {
	if l == nil || l.raw == nil {
		return nil, net.ErrClosed
	}

	connection, err := l.raw.AcceptUnix()
	if err != nil {
		return nil, err
	}

	return connection, nil
}

func (l *privateListener) Close() error {
	if l == nil {
		return nil
	}

	l.closeOnce.Do(func() {
		rawErr := l.raw.Close()
		removeErr := removeSocketGeneration(
			l.root,
			l.name,
			l.generation,
		)
		sourceErr := l.source.Close()
		rootErr := l.root.Close()
		l.closeErr = errors.Join(
			rawErr,
			removeErr,
			sourceErr,
			rootErr,
		)
	})

	return l.closeErr
}

func closeUnpublishedListener(
	operationErr error,
	raw *net.UnixListener,
	root *os.File,
	name string,
	generation socketGeneration,
	logger *diagnostic.Logger,
) error {
	logger.DebugError("close unpublished socket relay listener", raw.Close())
	if generation != (socketGeneration{}) {
		logger.DebugError(
			"remove unpublished socket relay endpoint",
			removeSocketGeneration(root, name, generation),
		)
	}
	logger.DebugError(
		"close unpublished socket relay runtime root",
		root.Close(),
	)

	return operationErr
}

func removeSocketGeneration(
	root *os.File,
	name string,
	generation socketGeneration,
) error {
	current, err := inspectSocket(root, name)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return err
	}
	if current != generation {
		return fmt.Errorf(
			"socket relay endpoint changed before cleanup",
		)
	}
	if err := unix.Unlinkat(int(root.Fd()), name, 0); err != nil &&
		!errors.Is(err, unix.ENOENT) {
		return &fs.PathError{
			Op:   "remove socket relay endpoint",
			Path: name,
			Err:  err,
		}
	}

	return nil
}
