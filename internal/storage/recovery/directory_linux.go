//go:build linux

package recovery

// Removes abandoned direct-child publication directories while excluding live
// publishers through their inode flock.

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/storage/safefs"
)

// CleanupTemporaryDirectories removes abandoned direct-child publication
// directories. maxCandidates bounds one scan and maxEntries bounds each
// recursive removal. A truncated scan makes progress before returning
// safefs.ErrLimitExceeded so later calls can resume.
func CleanupTemporaryDirectories(
	directory *safefs.Directory,
	maxCandidates uint64,
	maxEntries uint64,
) (returnErr error) {
	if directory == nil {
		return fmt.Errorf("temporary recovery directory must not be nil")
	}
	if maxCandidates == 0 {
		return fmt.Errorf("temporary recovery candidate limit must be positive")
	}
	if maxEntries == 0 {
		return fmt.Errorf("temporary recovery entry limit must be positive")
	}

	file, parentStat, err := openRecoveryDirectory(directory)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, closeFile(file, "close temporary recovery directory"))
	}()
	locked, err := lockRecoveryParent(file, directory.Path())
	if err != nil {
		return err
	}
	if !locked {
		return nil
	}
	lockHeld := true
	defer func() {
		if lockHeld {
			returnErr = errors.Join(returnErr, unlockRecoveryParent(file, directory.Path()))
		}
	}()

	candidates, limitExceeded, err := scanTemporaryNames(file, directory.Path(), maxCandidates)
	if err != nil {
		return err
	}
	if err := unlockRecoveryParent(file, directory.Path()); err != nil {
		return err
	}
	lockHeld = false

	for _, name := range candidates {
		if err := cleanupTemporaryDirectory(
			directory,
			int(file.Fd()),
			name,
			&parentStat,
			maxEntries,
		); err != nil {
			return err
		}
	}

	if limitExceeded {
		return fmt.Errorf(
			"%w: temporary recovery in %q exceeds %d candidates",
			safefs.ErrLimitExceeded,
			directory.Path(),
			maxCandidates,
		)
	}
	return nil
}

func openRecoveryDirectory(
	directory *safefs.Directory,
) (*os.File, unix.Stat_t, error) {
	authority, err := directory.File()
	if err != nil {
		return nil, unix.Stat_t{}, fmt.Errorf("open temporary recovery directory: %w", err)
	}
	fd, err := unix.Openat(
		int(authority.Fd()),
		".",
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	authorityCloseErr := closeFile(authority, "close temporary recovery authority")
	if err != nil {
		return nil, unix.Stat_t{}, errors.Join(
			&fs.PathError{Op: "reopen temporary recovery directory", Path: directory.Path(), Err: err},
			authorityCloseErr,
		)
	}
	if authorityCloseErr != nil {
		return nil, unix.Stat_t{}, errors.Join(
			authorityCloseErr,
			closeDescriptor(fd, directory.Path()),
		)
	}

	file := os.NewFile(uintptr(fd), directory.Path())
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, unix.Stat_t{}, errors.Join(
			&fs.PathError{Op: "stat temporary recovery directory", Path: directory.Path(), Err: err},
			closeFile(file, "close temporary recovery directory"),
		)
	}
	return file, stat, nil
}

// lockRecoveryParent excludes publication from its first temporary-name
// creation through its final rename and sync. A live publisher causes recovery
// to skip this pass; a later startup or use can safely recover its artifacts.
func lockRecoveryParent(file *os.File, path string) (bool, error) {
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EWOULDBLOCK) {
			return false, nil
		}
		if err != nil {
			return false, &fs.PathError{
				Op:   "lock temporary recovery directory",
				Path: path,
				Err:  err,
			}
		}
		return true, nil
	}
}

func unlockRecoveryParent(file *os.File, path string) error {
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_UN)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return &fs.PathError{
				Op:   "unlock temporary recovery directory",
				Path: path,
				Err:  err,
			}
		}
		return nil
	}
}

func scanTemporaryNames(
	file *os.File,
	path string,
	maxCandidates uint64,
) ([]string, bool, error) {
	candidates := make([]string, 0)
	for {
		names, readErr := file.Readdirnames(recoveryReadBatch)
		for _, name := range names {
			if !isTemporaryName(name) {
				continue
			}
			if uint64(len(candidates)) == maxCandidates {
				return candidates, true, nil
			}
			candidates = append(candidates, name)
		}

		if errors.Is(readErr, io.EOF) {
			return candidates, false, nil
		}
		if readErr != nil {
			return nil, false, &fs.PathError{
				Op:   "read temporary recovery directory",
				Path: path,
				Err:  readErr,
			}
		}
	}
}

func cleanupTemporaryDirectory(
	directory *safefs.Directory,
	parentFD int,
	name string,
	parentStat *unix.Stat_t,
	maxEntries uint64,
) (returnErr error) {
	path := filepath.Join(directory.Path(), name)
	pathFD, err := unix.Openat(
		parentFD,
		name,
		unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return unsafeTemporary(path, "open", err)
	}
	defer func() {
		if err := unix.Close(pathFD); err != nil {
			returnErr = errors.Join(
				returnErr,
				&fs.PathError{Op: "close temporary recovery path", Path: path, Err: err},
			)
		}
	}()

	var initial unix.Stat_t
	if err := unix.Fstat(pathFD, &initial); err != nil {
		return &fs.PathError{Op: "stat temporary recovery directory", Path: path, Err: err}
	}
	identity, err := validateTemporaryDirectory(&initial, parentStat, path)
	if err != nil {
		return err
	}
	if int(initial.Uid) == os.Geteuid() && initial.Mode&0o700 != 0o700 {
		mode := uint32(initial.Mode&0o7777) | 0o700
		if err := unix.Fchmodat(
			pathFD,
			"",
			mode,
			unix.AT_EMPTY_PATH,
		); err != nil {
			return &fs.PathError{
				Op:   "make temporary recovery directory accessible",
				Path: path,
				Err:  err,
			}
		}
	}

	fd, err := unix.Openat(
		pathFD,
		".",
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return &fs.PathError{Op: "open widened temporary recovery directory", Path: path, Err: err}
	}
	defer func() {
		if err := unix.Close(fd); err != nil {
			returnErr = errors.Join(
				returnErr,
				&fs.PathError{Op: "close temporary recovery directory", Path: path, Err: err},
			)
		}
	}()

	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil {
		return &fs.PathError{Op: "stat opened temporary recovery directory", Path: path, Err: err}
	}
	openedIdentity, err := validateTemporaryDirectory(&opened, parentStat, path)
	if err != nil {
		return err
	}
	if openedIdentity != identity {
		return fmt.Errorf(
			"%w: temporary recovery directory %q changed identity while opening",
			safefs.ErrUnsafePath,
			path,
		)
	}

	for {
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	if errors.Is(err, unix.EWOULDBLOCK) {
		return nil
	}
	if err != nil {
		return &fs.PathError{Op: "lock temporary recovery directory", Path: path, Err: err}
	}

	var locked unix.Stat_t
	if err := unix.Fstat(fd, &locked); err != nil {
		return &fs.PathError{Op: "stat locked temporary recovery directory", Path: path, Err: err}
	}
	lockedIdentity, err := validateTemporaryDirectory(&locked, parentStat, path)
	if err != nil {
		return err
	}
	if lockedIdentity != identity {
		return fmt.Errorf(
			"%w: temporary recovery directory %q changed identity",
			safefs.ErrUnsafePath,
			path,
		)
	}

	var named unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return &fs.PathError{Op: "stat temporary recovery name", Path: path, Err: err}
	}
	namedIdentity, err := validateTemporaryDirectory(&named, parentStat, path)
	if err != nil {
		return err
	}
	if namedIdentity != identity {
		return fmt.Errorf(
			"%w: temporary recovery directory %q was replaced",
			safefs.ErrUnsafePath,
			path,
		)
	}

	if err := directory.RemoveAllOwned(name, maxEntries); err != nil {
		return fmt.Errorf("remove temporary recovery directory %q: %w", path, err)
	}
	return nil
}

func validateTemporaryDirectory(
	stat *unix.Stat_t,
	parentStat *unix.Stat_t,
	path string,
) (fileIdentity, error) {
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fileIdentity{}, fmt.Errorf(
			"%w: temporary recovery target %q is not a directory",
			safefs.ErrUnsafePath,
			path,
		)
	}
	if stat.Dev != parentStat.Dev {
		return fileIdentity{}, fmt.Errorf(
			"%w: temporary recovery target %q is on device %d, want %d",
			safefs.ErrUnsafePath,
			path,
			stat.Dev,
			parentStat.Dev,
		)
	}
	return fileIdentity{device: uint64(stat.Dev), inode: stat.Ino}, nil
}
