//go:build linux

package safefs

// Publishes durable files and populated directory trees without replacement.

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/diagnostic"
)

const temporaryNameAttempts = 16

// PublishFile durably publishes data as a regular file only when name does not
// exist. It returns false with a nil error when another publisher wins.
func (d *Directory) PublishFile(
	name string,
	data []byte,
	mode fs.FileMode,
) (bool, error) {
	permissions, err := publicationPermissions(mode)
	if err != nil {
		return false, err
	}

	parentFD, base, path, err := d.openParent(name)
	if err != nil {
		return false, err
	}
	defer closeDescriptor(d.logger, parentFD)
	parentLock, err := retainPublicationParentLock(
		parentFD,
		filepath.Dir(path),
		d.logger,
	)
	if err != nil {
		return false, err
	}
	defer func() {
		d.logger.DebugError(
			"release file publication parent lock",
			releasePublicationParentLock(
				&parentLock,
				filepath.Dir(path),
			),
			"path", path,
		)
	}()

	temporary, fd, lockFD, identity, err := createTemporaryFile(
		parentFD,
		filepath.Dir(path),
		d.logger,
	)
	if err != nil {
		return false, err
	}
	temporaryPath := filepath.Join(filepath.Dir(path), temporary)
	defer func() {
		d.logger.DebugError(
			"close temporary file lock",
			closeTemporaryLock(lockFD, temporaryPath),
			"path", temporaryPath,
		)
	}()
	if err := releasePublicationParentLock(&parentLock, filepath.Dir(path)); err != nil {
		closeDescriptor(d.logger, fd)
		d.logger.DebugError(
			"remove temporary file after parent lock release failed",
			cleanupTemporaryFile(
				parentFD,
				temporary,
				temporaryPath,
			),
			"path", temporaryPath,
		)
		return false, err
	}

	if err := writeFileDescriptor(fd, data, permissions, temporaryPath); err != nil {
		closeDescriptor(d.logger, fd)
		d.logger.DebugError(
			"remove temporary file after write failed",
			cleanupTemporaryFile(
				parentFD,
				temporary,
				temporaryPath,
			),
			"path", temporaryPath,
		)
		return false, err
	}
	if err := unix.Close(fd); err != nil {
		closeErr := &fs.PathError{
			Op:   "close temporary file",
			Path: temporaryPath,
			Err:  err,
		}
		d.logger.DebugError(
			"remove temporary file after close failed",
			cleanupTemporaryFile(
				parentFD,
				temporary,
				temporaryPath,
			),
			"path", temporaryPath,
		)
		return false, closeErr
	}
	if err := validateTemporaryFile(
		parentFD,
		temporary,
		temporaryPath,
		identity,
		d.logger,
	); err != nil {
		d.logger.DebugError(
			"remove invalid temporary file",
			cleanupTemporaryFile(
				parentFD,
				temporary,
				temporaryPath,
			),
			"path", temporaryPath,
		)
		return false, err
	}

	err = renameNoReplace(parentFD, temporary, base)
	if errors.Is(err, unix.EEXIST) {
		d.logger.DebugError(
			"remove unpublished temporary file",
			cleanupTemporaryFile(
				parentFD,
				temporary,
				temporaryPath,
			),
			"path", temporaryPath,
		)
		return false, nil
	}
	if err != nil {
		publishErr := &fs.PathError{
			Op:   "publish file",
			Path: path,
			Err:  err,
		}
		d.logger.DebugError(
			"remove temporary file after publication failed",
			cleanupTemporaryFile(
				parentFD,
				temporary,
				temporaryPath,
			),
			"path", temporaryPath,
		)
		return false, publishErr
	}

	if err := unix.Fsync(parentFD); err != nil {
		return true, &fs.PathError{Op: "sync directory", Path: filepath.Dir(path), Err: err}
	}
	return true, nil
}

// ReplaceFile durably replaces a non-directory destination entry with complete
// data. An existing entry is replaced without being followed.
func (d *Directory) ReplaceFile(
	name string,
	data []byte,
	mode fs.FileMode,
) error {
	return d.replaceFile(name, data, mode, fileOwnership{})
}

// ReplaceFileOwned durably replaces a non-directory destination entry with
// complete data and best-effort ownership. Existing filesystem ownership does
// not prevent replacement when the containing directory permits it.
func (d *Directory) ReplaceFileOwned(
	name string,
	data []byte,
	mode fs.FileMode,
	uid, gid int,
) error {
	if err := validateFileOwnership(uid, gid); err != nil {
		return err
	}
	if d == nil {
		return fmt.Errorf("%w: replacement directory is nil", ErrUnsafePath)
	}

	return d.replaceFile(name, data, mode, fileOwnership{
		exact: true,
		uid:   uid,
		gid:   gid,
	})
}

type fileOwnership struct {
	exact bool
	uid   int
	gid   int
}

func (d *Directory) replaceFile(
	name string,
	data []byte,
	mode fs.FileMode,
	ownership fileOwnership,
) error {
	permissions, err := publicationPermissions(mode)
	if err != nil {
		return err
	}

	parentFD, base, path, err := d.openParent(name)
	if err != nil {
		return err
	}
	defer closeDescriptor(d.logger, parentFD)
	parentLock, err := retainPublicationParentLock(
		parentFD,
		filepath.Dir(path),
		d.logger,
	)
	if err != nil {
		return err
	}
	defer func() {
		d.logger.DebugError(
			"release file replacement parent lock",
			releasePublicationParentLock(
				&parentLock,
				filepath.Dir(path),
			),
			"path", path,
		)
	}()

	temporary, fd, lockFD, identity, err := createTemporaryFile(
		parentFD,
		filepath.Dir(path),
		d.logger,
	)
	if err != nil {
		return err
	}
	temporaryPath := filepath.Join(filepath.Dir(path), temporary)
	defer func() {
		d.logger.DebugError(
			"close replacement temporary file lock",
			closeTemporaryLock(lockFD, temporaryPath),
			"path", temporaryPath,
		)
	}()
	if err := releasePublicationParentLock(&parentLock, filepath.Dir(path)); err != nil {
		closeDescriptor(d.logger, fd)
		d.logger.DebugError(
			"remove replacement temporary file after parent lock release failed",
			cleanupTemporaryFile(
				parentFD,
				temporary,
				temporaryPath,
			),
			"path", temporaryPath,
		)
		return err
	}

	if err := d.writeReplacementDescriptor(
		fd,
		data,
		permissions,
		temporaryPath,
		ownership,
	); err != nil {
		closeDescriptor(d.logger, fd)
		d.logger.DebugError(
			"remove replacement temporary file after write failed",
			cleanupTemporaryFile(
				parentFD,
				temporary,
				temporaryPath,
			),
			"path", temporaryPath,
		)
		return err
	}
	if err := unix.Close(fd); err != nil {
		closeErr := &fs.PathError{
			Op:   "close temporary file",
			Path: temporaryPath,
			Err:  err,
		}
		d.logger.DebugError(
			"remove replacement temporary file after close failed",
			cleanupTemporaryFile(
				parentFD,
				temporary,
				temporaryPath,
			),
			"path", temporaryPath,
		)
		return closeErr
	}

	if err := validateTemporaryFile(
		parentFD,
		temporary,
		temporaryPath,
		identity,
		d.logger,
	); err != nil {
		d.logger.DebugError(
			"remove invalid replacement temporary file",
			cleanupTemporaryFile(
				parentFD,
				temporary,
				temporaryPath,
			),
			"path", temporaryPath,
		)
		return err
	}
	if err := unix.Renameat(parentFD, temporary, parentFD, base); err != nil {
		replaceErr := &fs.PathError{
			Op:   "replace file",
			Path: path,
			Err:  err,
		}
		d.logger.DebugError(
			"remove temporary file after replacement failed",
			cleanupTemporaryFile(
				parentFD,
				temporary,
				temporaryPath,
			),
			"path", temporaryPath,
		)
		return replaceErr
	}
	if err := unix.Fsync(parentFD); err != nil {
		return &fs.PathError{Op: "sync directory", Path: filepath.Dir(path), Err: err}
	}
	return nil
}

// PublishDirectory creates a private sibling stage, passes its retained
// capability to populate, and durably renames it into place only when name does
// not exist. cleanupLimit bounds removal after a loss or population failure.
func (d *Directory) PublishDirectory(
	name string,
	cleanupLimit uint64,
	populate func(*Directory) error,
) (bool, error) {
	if cleanupLimit == 0 {
		return false, fmt.Errorf("directory publication cleanup limit must be positive")
	}
	if populate == nil {
		return false, fmt.Errorf("directory publication populate function must not be nil")
	}

	parentFD, base, path, err := d.openParent(name)
	if err != nil {
		return false, err
	}
	defer closeDescriptor(d.logger, parentFD)
	parentLock, err := retainPublicationParentLock(
		parentFD,
		filepath.Dir(path),
		d.logger,
	)
	if err != nil {
		return false, err
	}
	defer func() {
		d.logger.DebugError(
			"release directory publication parent lock",
			releasePublicationParentLock(
				&parentLock,
				filepath.Dir(path),
			),
			"path", path,
		)
	}()

	temporary, err := createTemporaryDirectory(parentFD, filepath.Dir(path))
	if err != nil {
		return false, err
	}
	temporaryPath := filepath.Join(filepath.Dir(path), temporary)

	stageFD, err := repairPrivateDirectory(
		parentFD,
		temporary,
		temporaryPath,
		d.options(),
	)
	if err != nil {
		openErr := unsafeComponentError(
			"open temporary directory",
			temporaryPath,
			err,
		)
		d.logger.DebugError(
			"remove temporary directory after open failed",
			cleanupTemporaryDirectory(parentFD, temporary, temporaryPath, cleanupLimit),
			"path", temporaryPath,
		)
		return false, openErr
	}
	if err := unix.Fsync(parentFD); err != nil {
		closeDescriptor(d.logger, stageFD)
		syncErr := &fs.PathError{
			Op:   "sync temporary directory parent",
			Path: filepath.Dir(path),
			Err:  err,
		}
		d.logger.DebugError(
			"remove temporary directory after parent sync failed",
			cleanupTemporaryDirectory(parentFD, temporary, temporaryPath, cleanupLimit),
			"path", temporaryPath,
		)
		return false, syncErr
	}
	lockFD, err := retainTemporaryDirectoryLock(
		stageFD,
		temporaryPath,
		d.logger,
	)
	if err != nil {
		closeDescriptor(d.logger, stageFD)
		d.logger.DebugError(
			"remove temporary directory after lock setup failed",
			cleanupTemporaryDirectory(parentFD, temporary, temporaryPath, cleanupLimit),
			"path", temporaryPath,
		)
		return false, err
	}
	defer func() {
		d.logger.DebugError(
			"close temporary directory lock",
			closeTemporaryLock(lockFD, temporaryPath),
			"path", temporaryPath,
		)
	}()
	if err := releasePublicationParentLock(&parentLock, filepath.Dir(path)); err != nil {
		closeDescriptor(d.logger, stageFD)
		d.logger.DebugError(
			"remove temporary directory after parent lock release failed",
			cleanupTemporaryDirectory(parentFD, temporary, temporaryPath, cleanupLimit),
			"path", temporaryPath,
		)
		return false, err
	}

	stage := newDirectory(stageFD, temporaryPath, d.options())
	if err := validateDirectory(stageFD, temporaryPath); err != nil {
		d.logger.DebugError(
			"close invalid temporary directory",
			stage.Close(),
			"path", temporaryPath,
		)
		d.logger.DebugError(
			"remove invalid temporary directory",
			cleanupTemporaryDirectory(
				parentFD,
				temporary,
				temporaryPath,
				cleanupLimit,
			),
			"path", temporaryPath,
		)
		return false, err
	}
	identity, err := descriptorIdentity(stageFD, temporaryPath)
	if err != nil {
		d.logger.DebugError(
			"close temporary directory after identity lookup failed",
			stage.Close(),
			"path", temporaryPath,
		)
		d.logger.DebugError(
			"remove temporary directory after identity lookup failed",
			cleanupTemporaryDirectory(parentFD, temporary, temporaryPath, cleanupLimit),
			"path", temporaryPath,
		)
		return false, err
	}

	if err := populate(stage); err != nil {
		d.logger.DebugError(
			"close temporary directory after population failed",
			stage.Close(),
			"path", temporaryPath,
		)
		d.logger.DebugError(
			"remove temporary directory after population failed",
			cleanupTemporaryDirectory(
				parentFD,
				temporary,
				temporaryPath,
				cleanupLimit,
			),
			"path", temporaryPath,
		)
		return false, fmt.Errorf("populate %q: %w", path, err)
	}
	if err := stage.Sync(); err != nil {
		d.logger.DebugError(
			"close temporary directory after sync failed",
			stage.Close(),
			"path", temporaryPath,
		)
		d.logger.DebugError(
			"remove temporary directory after sync failed",
			cleanupTemporaryDirectory(
				parentFD,
				temporary,
				temporaryPath,
				cleanupLimit,
			),
			"path", temporaryPath,
		)
		return false, err
	}
	d.logger.DebugError(
		"close populated temporary directory",
		stage.Close(),
		"path", temporaryPath,
	)
	if err := validateTemporaryDirectory(
		parentFD,
		temporary,
		temporaryPath,
		identity,
		d.logger,
	); err != nil {
		d.logger.DebugError(
			"remove invalid populated temporary directory",
			cleanupTemporaryDirectory(parentFD, temporary, temporaryPath, cleanupLimit),
			"path", temporaryPath,
		)
		return false, err
	}

	err = renameNoReplace(parentFD, temporary, base)
	if errors.Is(err, unix.EEXIST) {
		d.logger.DebugError(
			"remove unpublished temporary directory",
			cleanupTemporaryDirectory(
				parentFD,
				temporary,
				temporaryPath,
				cleanupLimit,
			),
			"path", temporaryPath,
		)
		return false, nil
	}
	if err != nil {
		publishErr := &fs.PathError{
			Op:   "publish directory",
			Path: path,
			Err:  err,
		}
		d.logger.DebugError(
			"remove temporary directory after publication failed",
			cleanupTemporaryDirectory(parentFD, temporary, temporaryPath, cleanupLimit),
			"path", temporaryPath,
		)
		return false, publishErr
	}

	if err := unix.Fsync(parentFD); err != nil {
		return true, &fs.PathError{Op: "sync directory", Path: filepath.Dir(path), Err: err}
	}
	return true, nil
}

type descriptorID struct {
	device uint64
	inode  uint64
}

func createTemporaryFile(
	parentFD int,
	parentPath string,
	logger *diagnostic.Logger,
) (string, int, int, descriptorID, error) {
	for range temporaryNameAttempts {
		name, err := temporaryName()
		if err != nil {
			return "", -1, -1, descriptorID{}, err
		}

		fd, err := openRelative(
			parentFD,
			name,
			unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NONBLOCK,
			0o600,
			logger,
		)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return "", -1, -1, descriptorID{}, &fs.PathError{
				Op:   "create temporary file",
				Path: filepath.Join(parentPath, name),
				Err:  err,
			}
		}
		if err := unix.Fchmod(fd, 0o600); err != nil {
			closeDescriptor(logger, fd)
			path := filepath.Join(parentPath, name)
			modeErr := &fs.PathError{
				Op:   "set temporary file mode",
				Path: path,
				Err:  err,
			}
			logger.DebugError(
				"remove temporary file after mode setup failed",
				cleanupTemporaryFile(
					parentFD,
					name,
					path,
				),
				"path", path,
			)
			return "", -1, -1, descriptorID{}, modeErr
		}

		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); err != nil {
			closeDescriptor(logger, fd)
			path := filepath.Join(parentPath, name)
			statErr := &fs.PathError{
				Op:   "stat temporary file",
				Path: path,
				Err:  err,
			}
			logger.DebugError(
				"remove temporary file after inspection failed",
				cleanupTemporaryFile(parentFD, name, path),
				"path", path,
			)
			return "", -1, -1, descriptorID{}, statErr
		}
		path := filepath.Join(parentPath, name)
		if err := validateRegularStat(&stat, path); err != nil {
			closeDescriptor(logger, fd)
			logger.DebugError(
				"remove invalid temporary file",
				cleanupTemporaryFile(parentFD, name, path),
				"path", path,
			)
			return "", -1, -1, descriptorID{}, err
		}
		lockFD, err := retainTemporaryLock(fd, path, logger)
		if err != nil {
			closeDescriptor(logger, fd)
			logger.DebugError(
				"remove temporary file after lock setup failed",
				cleanupTemporaryFile(parentFD, name, path),
				"path", path,
			)
			return "", -1, -1, descriptorID{}, err
		}
		return name, fd, lockFD, descriptorID{device: uint64(stat.Dev), inode: stat.Ino}, nil
	}

	return "", -1, -1, descriptorID{}, fmt.Errorf(
		"create temporary file: exhausted %d collision attempts",
		temporaryNameAttempts,
	)
}

func publicationPermissions(mode fs.FileMode) (uint32, error) {
	permissions, err := regularPermissions(mode)
	if err != nil {
		return 0, err
	}
	if permissions&0o600 == 0 {
		return 0, fmt.Errorf(
			"publication mode %04o must grant owner read or write permission",
			permissions,
		)
	}
	return permissions, nil
}

func retainTemporaryLock(
	fd int,
	path string,
	logger *diagnostic.Logger,
) (int, error) {
	return retainTemporaryLockMode(fd, path, unix.LOCK_EX, logger)
}

func retainTemporaryDirectoryLock(
	fd int,
	path string,
	logger *diagnostic.Logger,
) (int, error) {
	return retainTemporaryLockMode(fd, path, unix.LOCK_SH, logger)
}

func retainTemporaryLockMode(
	fd int,
	path string,
	mode int,
	logger *diagnostic.Logger,
) (int, error) {
	lockFD, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return -1, &fs.PathError{Op: "retain temporary lock", Path: path, Err: err}
	}
	if err := unix.Flock(lockFD, mode|unix.LOCK_NB); err != nil {
		logger.DebugError(
			"close temporary object lock after lock failed",
			closeTemporaryLock(lockFD, path),
			"path", path,
		)
		return -1, &fs.PathError{
			Op:   "lock temporary object",
			Path: path,
			Err:  err,
		}
	}
	return lockFD, nil
}

func closeTemporaryLock(fd int, path string) error {
	if err := unix.Close(fd); err != nil {
		return &fs.PathError{Op: "close temporary lock", Path: path, Err: err}
	}
	return nil
}

func retainPublicationParentLock(
	parentFD int,
	path string,
	logger *diagnostic.Logger,
) (int, error) {
	fd, err := unix.Openat(
		parentFD,
		".",
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return -1, &fs.PathError{Op: "open publication parent lock", Path: path, Err: err}
	}
	for {
		err = unix.Flock(fd, unix.LOCK_SH)
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	if err != nil {
		logger.DebugError(
			"close publication parent lock after lock failed",
			closePublicationParentLock(fd, path),
			"path", path,
		)
		return -1, &fs.PathError{
			Op:   "lock publication parent",
			Path: path,
			Err:  err,
		}
	}
	return fd, nil
}

func closePublicationParentLock(fd int, path string) error {
	if err := unix.Close(fd); err != nil {
		return &fs.PathError{Op: "close publication parent lock", Path: path, Err: err}
	}
	return nil
}

func releasePublicationParentLock(fd *int, path string) error {
	if *fd < 0 {
		return nil
	}

	owned := *fd
	*fd = -1
	return closePublicationParentLock(owned, path)
}

func createTemporaryDirectory(parentFD int, parentPath string) (string, error) {
	for range temporaryNameAttempts {
		name, err := temporaryName()
		if err != nil {
			return "", err
		}

		err = unix.Mkdirat(parentFD, name, 0o700)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return "", &fs.PathError{Op: "create temporary directory", Path: filepath.Join(parentPath, name), Err: err}
		}
		return name, nil
	}

	return "", fmt.Errorf("create temporary directory: exhausted %d collision attempts", temporaryNameAttempts)
}

func temporaryName() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate temporary name: %w", err)
	}
	return ".toby-tmp-" + hex.EncodeToString(random[:]), nil
}

func renameNoReplace(parentFD int, oldName, newName string) error {
	err := unix.Renameat2(parentFD, oldName, parentFD, newName, unix.RENAME_NOREPLACE)
	if openat2Unavailable(err) {
		return fmt.Errorf("%w: rename without replacement: %v", ErrUnsupported, err)
	}
	return err
}

func descriptorIdentity(fd int, path string) (descriptorID, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return descriptorID{}, &fs.PathError{Op: "stat temporary object", Path: path, Err: err}
	}
	return descriptorID{device: uint64(stat.Dev), inode: stat.Ino}, nil
}

func validateTemporaryFile(
	parentFD int,
	name, path string,
	expected descriptorID,
	logger *diagnostic.Logger,
) error {
	fd, err := openRelative(parentFD, name, unix.O_PATH, 0, logger)
	if err != nil {
		return unsafeComponentError("reopen temporary file", path, err)
	}
	defer closeDescriptor(logger, fd)

	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return &fs.PathError{Op: "stat temporary file", Path: path, Err: err}
	}
	if err := validateRegularStat(&stat, path); err != nil {
		return err
	}
	actual := descriptorID{device: uint64(stat.Dev), inode: stat.Ino}
	if actual != expected {
		return fmt.Errorf("%w: temporary file %q was replaced before publication", ErrUnsafePath, path)
	}
	return nil
}

func validateFileOwnership(uid, gid int) error {
	const omittedLinuxID = uint64(^uint32(0))

	if uid < 0 || uint64(uid) >= omittedLinuxID {
		return fmt.Errorf("%w: invalid replacement UID %d", ErrUnsafePath, uid)
	}
	if gid < 0 || uint64(gid) >= omittedLinuxID {
		return fmt.Errorf("%w: invalid replacement GID %d", ErrUnsafePath, gid)
	}
	return nil
}

func (d *Directory) writeReplacementDescriptor(
	fd int,
	data []byte,
	mode uint32,
	path string,
	ownership fileOwnership,
) error {
	if ownership.exact {
		if err := unix.Fchown(fd, ownership.uid, ownership.gid); err != nil {
			d.logger.DebugError(
				"set generated file ownership",
				err,
				"path", path,
				"desired_uid", ownership.uid,
				"desired_gid", ownership.gid,
			)
		}
	}

	return writeFileDescriptor(fd, data, mode, path)
}

func validateTemporaryDirectory(
	parentFD int,
	name, path string,
	expected descriptorID,
	logger *diagnostic.Logger,
) error {
	fd, err := openRelative(
		parentFD,
		name,
		unix.O_RDONLY|unix.O_DIRECTORY,
		0,
		logger,
	)
	if err != nil {
		return unsafeComponentError("reopen temporary directory", path, err)
	}
	defer closeDescriptor(logger, fd)

	if err := validateDirectory(fd, path); err != nil {
		return err
	}
	actual, err := descriptorIdentity(fd, path)
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("%w: temporary directory %q was replaced before publication", ErrUnsafePath, path)
	}
	return nil
}

func cleanupTemporaryFile(parentFD int, name, path string) error {
	if err := unix.Unlinkat(parentFD, name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return &fs.PathError{Op: "remove temporary file", Path: path, Err: err}
	}
	if err := unix.Fsync(parentFD); err != nil {
		return &fs.PathError{Op: "sync directory", Path: filepath.Dir(path), Err: err}
	}
	return nil
}

func cleanupTemporaryDirectory(parentFD int, name, path string, limit uint64) error {
	state, err := newRemovalState(parentFD, filepath.Dir(path), limit)
	if err != nil {
		return err
	}
	if err := removeEntry(parentFD, name, path, &state); err != nil {
		return err
	}
	if err := unix.Fsync(parentFD); err != nil {
		return &fs.PathError{Op: "sync directory", Path: filepath.Dir(path), Err: err}
	}
	return nil
}
