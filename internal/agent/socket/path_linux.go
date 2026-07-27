//go:build linux

package socket

// Opens accessible socket parent directories while retaining the exact
// resulting directory descriptor.

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/storage/safefs"
)

const (
	privateFileMode    = uint32(0o600)
	maxUnixPathBytes   = 107
	maxFilenameBytes   = 255
	electionLockSuffix = ".lock"
)

type endpointDirectory struct {
	file     *os.File
	fd       int
	path     string
	name     string
	lockName string
	uid      uint32
	gid      uint32
	logger   *diagnostic.Logger
}

func openEndpointDirectory(
	path string,
	create bool,
	uid, gid uint32,
	options Options,
) (*endpointDirectory, error) {
	parent, name, err := validateSocketPath(path)
	if err != nil {
		return nil, err
	}

	var root *safefs.Directory
	directoryOptions := safefs.DirectoryOptions{
		OwnerUID: int(uid),
		OwnerGID: int(gid),
		Logger:   options.Logger,
	}
	if create {
		root, err = safefs.OpenOrCreateRoot(parent, directoryOptions)
	} else {
		root, err = safefs.OpenPrivateRoot(parent, directoryOptions)
	}
	if err != nil {
		return nil, unsafePathError(
			"open private socket directory",
			parent,
			err,
		)
	}

	file, err := root.File()
	if err != nil {
		options.Logger.DebugError(
			"close private socket directory after retention failure",
			root.Close(),
		)
		return nil, fmt.Errorf(
			"retain private socket directory %q: %w",
			parent,
			err,
		)
	}
	options.Logger.DebugError(
		"close retained private socket directory",
		root.Close(),
	)

	return &endpointDirectory{
		file:     file,
		fd:       int(file.Fd()),
		path:     parent,
		name:     name,
		lockName: "." + name + electionLockSuffix,
		uid:      uid,
		gid:      gid,
		logger:   options.Logger,
	}, nil
}

func validateSocketPath(path string) (string, string, error) {
	if path == "" || strings.IndexByte(path, 0) >= 0 || !filepath.IsAbs(path) {
		return "", "", fmt.Errorf("%w: socket path %q must be absolute", ErrUnsafePath, path)
	}
	if filepath.Clean(path) != path {
		return "", "", fmt.Errorf("%w: socket path %q must be clean", ErrUnsafePath, path)
	}
	if len(path) > maxUnixPathBytes {
		return "", "", fmt.Errorf(
			"%w: socket path is %d bytes; Linux pathname sockets permit at most %d",
			ErrUnsafePath,
			len(path),
			maxUnixPathBytes,
		)
	}

	parent := filepath.Dir(path)
	name := filepath.Base(path)
	if parent == "/" || name == "." || name == string(filepath.Separator) {
		return "", "", fmt.Errorf("%w: socket path %q needs a dedicated parent directory", ErrUnsafePath, path)
	}
	if len("."+name+electionLockSuffix) > maxFilenameBytes {
		return "", "", fmt.Errorf("%w: socket filename %q is too long", ErrUnsafePath, name)
	}

	return parent, name, nil
}

func unsafePathError(operation string, path string, err error) error {
	return fmt.Errorf("%w: %s %q: %w", ErrUnsafePath, operation, path, err)
}

func (d *endpointDirectory) close() error {
	if d == nil || d.file == nil {
		return nil
	}

	err := d.file.Close()
	d.file = nil
	d.fd = -1
	if err != nil {
		return &fs.PathError{Op: "close private socket directory", Path: d.path, Err: err}
	}

	return nil
}

func (d *endpointDirectory) descriptorPath(name string) (string, error) {
	path := fmt.Sprintf("/proc/self/fd/%d/%s", d.fd, name)
	if len(path) > maxUnixPathBytes {
		return "", fmt.Errorf(
			"%w: descriptor-relative socket path is %d bytes; Linux permits at most %d",
			ErrUnsafePath,
			len(path),
			maxUnixPathBytes,
		)
	}

	return path, nil
}
