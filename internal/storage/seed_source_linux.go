//go:build linux

package storage

// Retains exact immutable seed-tree descriptors without granting mutation
// authority or imposing private-directory modes on OCI content.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/storage/safefs"
)

func openImmutableSeedDirectory(
	root *os.File,
	description, relative string,
	logger *diagnostic.Logger,
) (*os.File, bool, error) {
	raw, err := root.SyscallConn()
	if err != nil {
		return nil, false, fmt.Errorf("access seed root %q: %w", description, err)
	}

	rootFD := -1
	var duplicateErr error
	if err := raw.Control(func(source uintptr) {
		rootFD, duplicateErr = unix.FcntlInt(source, unix.F_DUPFD_CLOEXEC, 0)
	}); err != nil {
		return nil, false, fmt.Errorf("retain seed root %q: %w", description, err)
	}
	if duplicateErr != nil {
		return nil, false, fmt.Errorf("retain seed root %q: %w", description, duplicateErr)
	}

	flags, err := unix.FcntlInt(uintptr(rootFD), unix.F_GETFL, 0)
	if err != nil {
		closeDescriptor(logger, rootFD)
		return nil, false, fmt.Errorf("inspect seed root %q: %w", description, err)
	}
	if flags&unix.O_PATH != 0 || flags&unix.O_ACCMODE != unix.O_RDONLY {
		closeDescriptor(logger, rootFD)
		return nil, false, fmt.Errorf(
			"%w: seed root %q is not an ordinary read-only descriptor",
			safefs.ErrUnsafePath,
			description,
		)
	}

	var rootStat unix.Stat_t
	if err := unix.Fstat(rootFD, &rootStat); err != nil {
		closeDescriptor(logger, rootFD)
		return nil, false, &fs.PathError{Op: "stat seed root", Path: description, Err: err}
	}
	if err := validateImmutableSeedDirectory(&rootStat, description); err != nil {
		closeDescriptor(logger, rootFD)
		return nil, false, err
	}
	if relative == "" {
		return os.NewFile(uintptr(rootFD), description), true, nil
	}

	childFD, err := unix.Openat2(rootFD, relative, &unix.OpenHow{
		Flags: unix.O_RDONLY |
			unix.O_DIRECTORY |
			unix.O_NOFOLLOW |
			unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH |
			unix.RESOLVE_NO_MAGICLINKS |
			unix.RESOLVE_NO_SYMLINKS |
			unix.RESOLVE_NO_XDEV,
	})
	closeDescriptor(logger, rootFD)
	if errors.Is(err, unix.ENOENT) {
		return nil, false, nil
	}
	if err != nil {
		if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) {
			return nil, false, fmt.Errorf(
				"secure immutable seed traversal is unsupported: %w",
				err,
			)
		}
		if errors.Is(err, unix.ENOTDIR) ||
			errors.Is(err, unix.ELOOP) ||
			errors.Is(err, unix.EXDEV) {
			return nil, false, fmt.Errorf(
				"%w: open immutable seed directory %q: %v",
				safefs.ErrUnsafePath,
				relative,
				err,
			)
		}
		return nil, false, &fs.PathError{
			Op:   "open immutable seed directory",
			Path: relative,
			Err:  err,
		}
	}

	var childStat unix.Stat_t
	if err := unix.Fstat(childFD, &childStat); err != nil {
		closeDescriptor(logger, childFD)
		return nil, false, &fs.PathError{Op: "stat immutable seed directory", Path: relative, Err: err}
	}
	if err := validateImmutableSeedDirectory(&childStat, relative); err != nil {
		closeDescriptor(logger, childFD)
		return nil, false, err
	}
	if childStat.Dev != rootStat.Dev {
		closeDescriptor(logger, childFD)
		return nil, false, fmt.Errorf(
			"%w: immutable seed directory %q crosses a filesystem boundary",
			safefs.ErrUnsafePath,
			relative,
		)
	}
	return os.NewFile(uintptr(childFD), description+"/"+relative), true, nil
}

func validateImmutableSeedDirectory(stat *unix.Stat_t, path string) error {
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("%w: immutable seed path %q is not a directory", safefs.ErrUnsafePath, path)
	}
	return nil
}
