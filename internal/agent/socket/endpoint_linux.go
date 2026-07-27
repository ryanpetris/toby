//go:build linux

package socket

// Creates private listeners and elects the agent listener.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

type socketGeneration struct {
	device uint64
	inode  uint64
}

// Elect atomically elects one listener for path. When another agent already
// owns the endpoint, Elect returns a connection to that agent.
func Elect(
	ctx context.Context,
	path string,
	options Options,
) (*Election, error) {
	return elect(
		ctx,
		path,
		uint32(os.Geteuid()),
		uint32(os.Getegid()),
		options,
	)
}

// Listen creates a new private listener at path without agent election or
// stale-endpoint recovery.
func Listen(path string, options Options) (*Listener, error) {
	return listen(
		path,
		uint32(os.Geteuid()),
		uint32(os.Getegid()),
		options,
	)
}

func listen(
	path string,
	uid, gid uint32,
	options Options,
) (*Listener, error) {
	directory, err := openEndpointDirectory(path, true, uid, gid, options)
	if err != nil {
		return nil, err
	}

	listener, err := directory.listen()
	if err != nil {
		directory.logger.DebugError(
			"close private socket directory after listen failure",
			directory.close(),
		)
		return nil, err
	}

	return listener, nil
}

func elect(
	ctx context.Context,
	path string,
	uid, gid uint32,
	options Options,
) (*Election, error) {
	bounded, cancel, err := boundedContext(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()

	directory, err := openEndpointDirectory(path, true, uid, gid, options)
	if err != nil {
		return nil, err
	}

	lock, err := directory.lock(bounded)
	if err != nil {
		directory.logger.DebugError(
			"close private socket directory after election failure",
			directory.close(),
		)
		return nil, fmt.Errorf("acquire agent election lock: %w", err)
	}

	for {
		if err := bounded.Err(); err != nil {
			return finishElection(nil, err, directory, lock)
		}

		generation, exists, err := directory.socketGeneration()
		if err != nil {
			return finishElection(nil, err, directory, lock)
		}
		if !exists {
			listener, err := directory.listen()
			if err != nil {
				return finishElection(nil, err, directory, lock)
			}
			listener.elected = true

			return finishElection(&Election{Listener: listener}, nil, directory, lock)
		}

		conn, err := directory.dial(bounded)
		if err == nil {
			return finishElection(&Election{Conn: conn}, nil, directory, lock)
		}
		if !errors.Is(err, unix.ECONNREFUSED) {
			return finishElection(
				nil,
				fmt.Errorf("connect existing private socket: %w", err),
				directory,
				lock,
			)
		}

		current, exists, err := directory.socketGeneration()
		if err != nil {
			return finishElection(nil, err, directory, lock)
		}
		if !exists || current != generation {
			continue
		}
		if err := unix.Unlinkat(directory.fd, directory.name, 0); err != nil {
			if errors.Is(err, unix.ENOENT) {
				continue
			}

			return finishElection(
				nil,
				&fs.PathError{Op: "remove stale private socket", Path: path, Err: err},
				directory,
				lock,
			)
		}
	}
}

func finishElection(
	election *Election,
	operationErr error,
	directory *endpointDirectory,
	lock *electionLock,
) (*Election, error) {
	lock.close()
	if election == nil || election.Listener == nil {
		directory.logger.DebugError(
			"close private socket election directory",
			directory.close(),
		)
	}

	if operationErr == nil {
		return election, nil
	}
	if election == nil {
		return nil, operationErr
	}

	if election.Conn != nil {
		directory.logger.DebugError(
			"close private socket connection after election failure",
			election.Conn.Close(),
		)
	}
	if election.Listener != nil {
		directory.logger.DebugError(
			"close private socket listener after election failure",
			election.Listener.Close(),
		)
	}

	return nil, operationErr
}

// Dial connects to path. An active socket is tried before Toby opens its
// election lock, which also supports systemd-owned endpoint directories.
func Dial(
	ctx context.Context,
	path string,
	options Options,
) (*net.UnixConn, error) {
	return dial(
		ctx,
		path,
		uint32(os.Geteuid()),
		uint32(os.Getegid()),
		options,
	)
}

func dial(
	ctx context.Context,
	path string,
	uid, gid uint32,
	options Options,
) (*net.UnixConn, error) {
	if _, _, err := validateSocketPath(path); err != nil {
		return nil, err
	}

	bounded, cancel, err := boundedContext(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()

	raw, directErr := (&net.Dialer{}).DialContext(
		bounded,
		"unix",
		path,
	)
	if directErr == nil {
		connection, ok := raw.(*net.UnixConn)
		if !ok {
			options.Logger.DebugError(
				"close rejected private socket connection",
				raw.Close(),
			)
			return nil, fmt.Errorf(
				"dial private socket: unexpected connection type %T",
				raw,
			)
		}

		return connection, nil
	}
	if !errors.Is(directErr, unix.ENOENT) &&
		!errors.Is(directErr, unix.ECONNREFUSED) {
		return nil, directErr
	}

	directory, err := openEndpointDirectory(path, false, uid, gid, options)
	if err != nil {
		return nil, err
	}
	lock, err := directory.lock(bounded)
	if err != nil {
		directory.logger.DebugError(
			"close private socket directory after connection lock failure",
			directory.close(),
		)
		return nil, fmt.Errorf("acquire agent connection lock: %w", err)
	}
	defer func() {
		directory.logger.DebugError(
			"release agent connection lock",
			lock.close(),
		)
		directory.logger.DebugError(
			"close private socket connection directory",
			directory.close(),
		)
	}()

	if _, exists, err := directory.socketGeneration(); err != nil {
		return nil, err
	} else if !exists {
		return nil, &net.OpError{
			Op:   "dial",
			Net:  "unix",
			Addr: &net.UnixAddr{Name: path, Net: "unix"},
			Err:  unix.ENOENT,
		}
	}

	return directory.dial(bounded)
}

func (d *endpointDirectory) listen() (*Listener, error) {
	descriptorPath, err := d.descriptorPath(d.name)
	if err != nil {
		return nil, err
	}

	raw, err := net.ListenUnix("unix", &net.UnixAddr{Name: descriptorPath, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("listen on private socket %q: %w", filepathForError(d), err)
	}
	raw.SetUnlinkOnClose(false)

	generation, exists, err := d.socketGeneration()
	if err != nil {
		d.logger.DebugError(
			"close private socket listener after inspection failure",
			raw.Close(),
		)
		return nil, err
	}
	if !exists {
		d.logger.DebugError(
			"close disappeared private socket listener",
			raw.Close(),
		)
		return nil, fmt.Errorf(
			"%w: private socket disappeared during creation",
			ErrUnsafePath,
		)
	}
	if err := d.chmodSocket(); err != nil {
		d.logger.DebugError(
			"correct private socket mode",
			err,
			"path", filepathForError(d),
			"desired_mode", "0600",
		)
	}

	secured, exists, err := d.socketGeneration()
	if err != nil {
		d.logger.DebugError(
			"close unsecured private socket listener",
			raw.Close(),
		)
		d.logger.DebugError(
			"remove unsecured private socket",
			d.removeGeneration(generation),
		)
		return nil, err
	}
	if !exists || secured != generation {
		d.logger.DebugError(
			"close replaced private socket listener",
			raw.Close(),
		)
		d.logger.DebugError(
			"remove replaced private socket generation",
			d.removeGeneration(generation),
		)
		return nil, fmt.Errorf(
			"%w: private socket changed while securing it",
			ErrUnsafePath,
		)
	}

	return &Listener{
		raw:        raw,
		directory:  d,
		generation: generation,
		address:    &net.UnixAddr{Name: filepathForError(d), Net: "unix"},
	}, nil
}

func (d *endpointDirectory) chmodSocket() error {
	err := unix.Fchmodat(d.fd, d.name, privateFileMode, unix.AT_SYMLINK_NOFOLLOW)
	if !errors.Is(err, unix.EOPNOTSUPP) {
		return err
	}

	// fchmodat2 supplies AT_SYMLINK_NOFOLLOW only on newer kernels. The
	// generation check after the fallback syscall rejects a replacement before
	// Toby publishes the listener.
	return unix.Fchmodat(d.fd, d.name, privateFileMode, 0)
}

func (d *endpointDirectory) dial(
	ctx context.Context,
) (*net.UnixConn, error) {
	descriptorPath, err := d.descriptorPath(d.name)
	if err != nil {
		return nil, err
	}

	dialer := net.Dialer{}
	raw, err := dialer.DialContext(ctx, "unix", descriptorPath)
	if err != nil {
		return nil, err
	}

	conn, ok := raw.(*net.UnixConn)
	if !ok {
		d.logger.DebugError(
			"close rejected private socket connection",
			raw.Close(),
		)
		return nil, fmt.Errorf(
			"dial private socket: unexpected connection type %T",
			raw,
		)
	}
	return conn, nil
}

func (d *endpointDirectory) socketGeneration() (socketGeneration, bool, error) {
	var stat unix.Stat_t
	err := unix.Fstatat(d.fd, d.name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return socketGeneration{}, false, nil
	}
	if err != nil {
		return socketGeneration{}, false, &fs.PathError{
			Op:   "inspect private socket",
			Path: filepathForError(d),
			Err:  err,
		}
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFSOCK {
		return socketGeneration{}, false, fmt.Errorf(
			"%w: private endpoint %q is not a Unix socket",
			ErrUnsafePath,
			filepathForError(d),
		)
	}

	return socketGeneration{device: uint64(stat.Dev), inode: stat.Ino}, true, nil
}

func (d *endpointDirectory) removeGeneration(generation socketGeneration) error {
	current, exists, err := d.socketGeneration()
	if err != nil || !exists || current != generation {
		return err
	}
	if err := unix.Unlinkat(d.fd, d.name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return &fs.PathError{Op: "remove private socket", Path: filepathForError(d), Err: err}
	}

	return nil
}

func filepathForError(d *endpointDirectory) string {
	return d.path + string(os.PathSeparator) + d.name
}
