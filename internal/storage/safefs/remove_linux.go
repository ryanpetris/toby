//go:build linux

package safefs

// Removes bounded directory trees through pinned filesystem authorities.

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/diagnostic"
)

const removalReadBatch = 64

type removalState struct {
	remaining uint64
	removed   uint64
	device    uint64
	mountID   uint64
	logger    *diagnostic.Logger
}

type removalIdentity struct {
	device   uint64
	inode    uint64
	fileType uint32
	mountID  uint64
}

// RemoveAll removes name and its descendants without following symbolic links.
// maxEntries counts name itself and every visited descendant. If the bound is
// exhausted, already completed removals remain completed.
//
// Toby owns every tree it removes, so each directory is widened to owner-only
// traversal before its entries are unlinked. Applications routinely leave
// read-only directories behind, such as the Go module cache, and those entries
// cannot be unlinked until their parent directory is writable again. Failed
// corrective chmod attempts emit debug diagnostics and continue; only a
// subsequent filesystem operation that actually needs the access stops
// removal.
func (d *Directory) RemoveAll(name string, maxEntries uint64) error {
	_, err := d.RemoveAllProgress(name, maxEntries)
	return err
}

// RemoveAllProgress removes name and its descendants like RemoveAll and
// reports how many entries this call actually unlinked.
func (d *Directory) RemoveAllProgress(
	name string,
	maxEntries uint64,
) (uint64, error) {
	if maxEntries == 0 {
		return 0, fmt.Errorf("removal limit must be positive")
	}

	rootFD, err := d.duplicateFD()
	if err != nil {
		return 0, err
	}
	state, err := newRemovalState(rootFD, d.Path(), maxEntries)
	closeRemovalDescriptor(d.logger, rootFD, d.Path())
	if err != nil {
		return state.removed, err
	}
	state.logger = d.logger

	parentFD, base, path, err := d.openRemovalParent(name, &state)
	if err != nil {
		return state.removed, err
	}
	defer closeDescriptor(d.logger, parentFD)

	if err := removeEntry(parentFD, base, path, &state); err != nil {
		return state.removed, err
	}
	if err := unix.Fsync(parentFD); err != nil {
		return state.removed, &fs.PathError{
			Op:   "sync directory",
			Path: filepath.Dir(path),
			Err:  err,
		}
	}
	return state.removed, nil
}

func (d *Directory) openRemovalParent(
	name string,
	state *removalState,
) (int, string, string, error) {
	components, err := validateRelativePath(name)
	if err != nil {
		return -1, "", "", err
	}

	currentFD, err := d.duplicateFD()
	if err != nil {
		return -1, "", "", err
	}
	currentPath := d.path
	if err := validateRemovalParent(currentFD, currentPath, state); err != nil {
		closeDescriptor(state.logger, currentFD)
		return -1, "", "", err
	}

	for _, component := range components[:len(components)-1] {
		nextPath := filepath.Join(currentPath, component)
		nextFD, err := openRelative(
			currentFD,
			component,
			unix.O_RDONLY|unix.O_DIRECTORY,
			0,
			state.logger,
		)
		if err != nil {
			closeDescriptor(state.logger, currentFD)
			return -1, "", "", unsafeComponentError("open removal parent", nextPath, err)
		}
		if err := validateRemovalParent(nextFD, nextPath, state); err != nil {
			closeDescriptor(state.logger, nextFD)
			closeDescriptor(state.logger, currentFD)
			return -1, "", "", err
		}

		closeDescriptor(state.logger, currentFD)
		currentFD = nextFD
		currentPath = nextPath
	}

	return currentFD, components[len(components)-1], filepath.Join(d.path, name), nil
}

func newRemovalState(
	directoryFD int,
	path string,
	maxEntries uint64,
) (removalState, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(directoryFD, &stat); err != nil {
		return removalState{}, &fs.PathError{Op: "stat removal authority", Path: path, Err: err}
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return removalState{}, fmt.Errorf("%w: removal authority %q is not a directory", ErrUnsafePath, path)
	}
	mountID, err := removalDescriptorMountID(directoryFD, path)
	if err != nil {
		return removalState{}, err
	}
	return removalState{
		remaining: maxEntries,
		device:    uint64(stat.Dev),
		mountID:   mountID,
	}, nil
}

func validateRemovalParent(fd int, path string, state *removalState) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return &fs.PathError{Op: "stat removal parent", Path: path, Err: err}
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("%w: removal parent %q is not a directory", ErrUnsafePath, path)
	}
	if uint64(stat.Dev) != state.device {
		return fmt.Errorf(
			"%w: removal parent %q is on device %d, want %d",
			ErrUnsafePath,
			path,
			stat.Dev,
			state.device,
		)
	}
	mountID, err := removalDescriptorMountID(fd, path)
	if err != nil {
		return err
	}
	if mountID != state.mountID {
		return fmt.Errorf(
			"%w: removal parent %q is on mount %d, want %d",
			ErrUnsafePath,
			path,
			mountID,
			state.mountID,
		)
	}
	return nil
}

func removeEntry(parentFD int, name, path string, state *removalState) error {
	targetFD, err := openRelative(
		parentFD,
		name,
		unix.O_PATH,
		0,
		state.logger,
	)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return unsafeComponentError("open removal target", path, err)
	}

	var stat unix.Stat_t
	if err := unix.Fstat(targetFD, &stat); err != nil {
		closeRemovalDescriptor(state.logger, targetFD, path)
		return &fs.PathError{
			Op:   "stat removal target",
			Path: path,
			Err:  err,
		}
	}
	if state.remaining == 0 {
		closeRemovalDescriptor(state.logger, targetFD, path)
		return fmt.Errorf("%w: removing %q", ErrLimitExceeded, path)
	}
	state.remaining--

	identity, err := validateRemovalDescriptor(targetFD, &stat, path, state)
	if err != nil {
		closeRemovalDescriptor(state.logger, targetFD, path)
		return err
	}

	if identity.fileType == unix.S_IFDIR {
		err = removeDirectory(parentFD, name, targetFD, identity, path, state)
	} else {
		err = removeNonDirectory(parentFD, name, identity, path, state)
	}
	closeRemovalDescriptor(state.logger, targetFD, path)
	return err
}

func validateRemovalTarget(
	stat *unix.Stat_t,
	mountID uint64,
	path string,
	state *removalState,
) (removalIdentity, error) {
	if uint64(stat.Dev) != state.device {
		return removalIdentity{}, fmt.Errorf(
			"%w: removal target %q is on device %d, want %d",
			ErrUnsafePath,
			path,
			stat.Dev,
			state.device,
		)
	}
	if mountID != state.mountID {
		return removalIdentity{}, fmt.Errorf(
			"%w: removal target %q is on mount %d, want %d",
			ErrUnsafePath,
			path,
			mountID,
			state.mountID,
		)
	}
	return removalIdentity{
		device:   uint64(stat.Dev),
		inode:    stat.Ino,
		fileType: stat.Mode & unix.S_IFMT,
		mountID:  mountID,
	}, nil
}

func validateRemovalDescriptor(
	fd int,
	stat *unix.Stat_t,
	path string,
	state *removalState,
) (removalIdentity, error) {
	mountID, err := removalDescriptorMountID(fd, path)
	if err != nil {
		return removalIdentity{}, err
	}
	return validateRemovalTarget(stat, mountID, path, state)
}

func removeNonDirectory(
	parentFD int,
	name string,
	identity removalIdentity,
	path string,
	state *removalState,
) error {
	present, err := validateRemovalName(parentFD, name, identity, path, state)
	if err != nil || !present {
		return err
	}

	if err := unix.Unlinkat(parentFD, name, 0); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		if errors.Is(err, unix.EISDIR) {
			return fmt.Errorf("%w: removal target %q changed type before unlink", ErrUnsafePath, path)
		}
		return &fs.PathError{Op: "remove file", Path: path, Err: err}
	}
	state.removed++
	return nil
}

func removeDirectory(
	parentFD int,
	name string,
	pathFD int,
	identity removalIdentity,
	path string,
	state *removalState,
) error {
	present, err := validateRemovalName(parentFD, name, identity, path, state)
	if err != nil || !present {
		return err
	}
	// Unlinking an entry requires write and search access on its directory,
	// regardless of the entry's own mode, so widen the directory first.
	if err := unix.Fchmodat(
		pathFD,
		"",
		0o700,
		unix.AT_EMPTY_PATH,
	); err != nil {
		state.logger.DebugError(
			"make Toby-owned directory removable",
			err,
			"path",
			path,
		)
	}

	for {
		present, err := validateRemovalName(parentFD, name, identity, path, state)
		if err != nil || !present {
			return err
		}

		childFD, err := openRemovalDirectory(pathFD, identity, path, state)
		if err != nil {
			return err
		}

		removeErr := removeDirectoryContents(childFD, path, state)
		if removeErr == nil {
			if err := unix.Fsync(childFD); err != nil {
				removeErr = &fs.PathError{Op: "sync removal directory", Path: path, Err: err}
			}
		}
		closeRemovalDescriptor(state.logger, childFD, path)
		if removeErr != nil {
			return removeErr
		}

		present, err = validateRemovalName(parentFD, name, identity, path, state)
		if err != nil || !present {
			return err
		}

		err = unix.Unlinkat(parentFD, name, unix.AT_REMOVEDIR)
		if err == nil {
			state.removed++
			return nil
		}
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		if errors.Is(err, unix.ENOTEMPTY) || errors.Is(err, unix.EEXIST) {
			continue
		}
		if errors.Is(err, unix.ENOTDIR) {
			return fmt.Errorf("%w: removal target %q changed type before unlink", ErrUnsafePath, path)
		}
		return &fs.PathError{Op: "remove directory", Path: path, Err: err}
	}
}

func openRemovalDirectory(
	pathFD int,
	identity removalIdentity,
	path string,
	state *removalState,
) (int, error) {
	fd, err := unix.Openat(
		pathFD,
		".",
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return -1, &fs.PathError{Op: "open widened removal directory", Path: path, Err: err}
	}

	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		closeRemovalDescriptor(state.logger, fd, path)
		return -1, &fs.PathError{
			Op:   "stat opened removal directory",
			Path: path,
			Err:  err,
		}
	}
	actual, err := validateRemovalDescriptor(fd, &stat, path, state)
	if err != nil {
		closeRemovalDescriptor(state.logger, fd, path)
		return -1, err
	}
	if actual != identity || actual.fileType != unix.S_IFDIR {
		closeRemovalDescriptor(state.logger, fd, path)
		return -1, fmt.Errorf(
			"%w: opened removal directory %q changed identity",
			ErrUnsafePath,
			path,
		)
	}
	return fd, nil
}

func removeDirectoryContents(directoryFD int, path string, state *removalState) error {
	duplicate, err := unix.FcntlInt(uintptr(directoryFD), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return &fs.PathError{Op: "duplicate removal directory", Path: path, Err: err}
	}

	directory := os.NewFile(uintptr(duplicate), path)
	defer closeFile(state.logger, directory)

	for {
		names, readErr := directory.Readdirnames(removalReadBatch)
		for _, name := range names {
			if err := removeEntry(directoryFD, name, filepath.Join(path, name), state); err != nil {
				return err
			}
		}

		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return &fs.PathError{Op: "read removal directory", Path: path, Err: readErr}
		}
	}
}

func validateRemovalName(
	parentFD int,
	name string,
	expected removalIdentity,
	path string,
	state *removalState,
) (bool, error) {
	actual, present, err := removalNameIdentity(parentFD, name, path, state)
	if err != nil || !present {
		return false, err
	}
	if actual != expected {
		return false, fmt.Errorf("%w: removal target %q was replaced", ErrUnsafePath, path)
	}
	return true, nil
}

func removalDescriptorMountID(fd int, path string) (uint64, error) {
	var stat unix.Statx_t
	if err := unix.Statx(
		fd,
		"",
		unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW,
		unix.STATX_MNT_ID,
		&stat,
	); err != nil {
		return 0, removalStatxError("inspect removal mount", path, err)
	}
	if stat.Mask&unix.STATX_MNT_ID == 0 {
		return 0, fmt.Errorf(
			"%w: mount identity is unavailable for removal target %q",
			ErrUnsupported,
			path,
		)
	}
	return stat.Mnt_id, nil
}

func removalNameIdentity(
	parentFD int,
	name string,
	path string,
	state *removalState,
) (removalIdentity, bool, error) {
	var stat unix.Statx_t
	if err := unix.Statx(
		parentFD,
		name,
		unix.AT_SYMLINK_NOFOLLOW,
		unix.STATX_BASIC_STATS|unix.STATX_MNT_ID,
		&stat,
	); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return removalIdentity{}, false, nil
		}
		return removalIdentity{}, false, removalStatxError("inspect removal name", path, err)
	}

	const required = unix.STATX_TYPE |
		unix.STATX_MODE |
		unix.STATX_INO |
		unix.STATX_MNT_ID
	if stat.Mask&required != required {
		return removalIdentity{}, false, fmt.Errorf(
			"%w: complete identity is unavailable for removal target %q",
			ErrUnsupported,
			path,
		)
	}

	device := unix.Mkdev(stat.Dev_major, stat.Dev_minor)
	if device != state.device {
		return removalIdentity{}, false, fmt.Errorf(
			"%w: removal target %q is on device %d, want %d",
			ErrUnsafePath,
			path,
			device,
			state.device,
		)
	}
	if stat.Mnt_id != state.mountID {
		return removalIdentity{}, false, fmt.Errorf(
			"%w: removal target %q is on mount %d, want %d",
			ErrUnsafePath,
			path,
			stat.Mnt_id,
			state.mountID,
		)
	}
	return removalIdentity{
		device:   device,
		inode:    stat.Ino,
		fileType: uint32(stat.Mode) & unix.S_IFMT,
		mountID:  stat.Mnt_id,
	}, true, nil
}

func removalStatxError(op, path string, err error) error {
	if errors.Is(err, unix.ENOSYS) ||
		errors.Is(err, unix.EINVAL) ||
		errors.Is(err, unix.EOPNOTSUPP) {
		return fmt.Errorf("%w: %s %q: %v", ErrUnsupported, op, path, err)
	}
	return &fs.PathError{Op: op, Path: path, Err: err}
}

func closeRemovalDescriptor(
	logger *diagnostic.Logger,
	fd int,
	path string,
) {
	err := unix.Close(fd)
	if err != nil {
		err = &fs.PathError{
			Op:   "close removal descriptor",
			Path: path,
			Err:  err,
		}
	}
	logger.DebugError(
		"close removal descriptor",
		err,
		"path", path,
	)
}
