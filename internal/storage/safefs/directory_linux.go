//go:build linux

package safefs

// Opens, validates, duplicates, and creates Linux directory capabilities.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/diagnostic"
)

const openResolveFlags = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_SYMLINKS

// OpenPrivateRoot follows accessible existing ancestry and retains the final
// directory. Toby best-effort repairs its owner and mode without changing its
// parents or any symlink used to reach it.
func OpenPrivateRoot(name string, options DirectoryOptions) (*Directory, error) {
	return openPrivateRoot(name, options, false)
}

// OpenOrCreateRoot follows accessible existing ancestry, creates missing
// parents with mode 0777 subject to the process umask, and retains the final
// directory. Only the resolved final directory is best-effort assigned to the
// configured owner and secured to mode 0700.
func OpenOrCreateRoot(name string, options DirectoryOptions) (*Directory, error) {
	return openPrivateRoot(name, options, true)
}

func openPrivateRoot(
	name string,
	options DirectoryOptions,
	create bool,
) (*Directory, error) {
	components, err := validateAbsolutePath(name)
	if err != nil {
		return nil, err
	}
	if err := validateDirectoryOptions(options); err != nil {
		return nil, err
	}
	if len(components) == 0 {
		return nil, fmt.Errorf(
			"%w: private root must not be the filesystem root",
			ErrUnsafePath,
		)
	}

	fd, err := unix.Open(
		"/",
		unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return nil, &fs.PathError{Op: "open root", Path: "/", Err: err}
	}

	currentPath := "/"
	for index, component := range components {
		nextPath := filepath.Join(currentPath, component)
		final := index == len(components)-1

		var nextFD int
		var openErr error
		if final {
			nextFD, _, openErr = openPrivateRootComponent(
				fd,
				component,
				nextPath,
				options,
				create,
			)
		} else {
			nextFD, _, openErr = openRootAncestor(
				fd,
				component,
				create,
			)
		}
		if openErr != nil {
			closeDescriptor(options.Logger, fd)
			return nil, unsafeComponentError(
				"open private root",
				nextPath,
				openErr,
			)
		}
		closeDescriptor(options.Logger, fd)
		fd = nextFD
		currentPath = nextPath
	}

	return newDirectory(fd, name, options), nil
}

func openRootAncestor(
	parentFD int,
	name string,
	create bool,
) (int, bool, error) {
	flags := unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC
	fd, err := unix.Openat(parentFD, name, flags, 0)
	if !create || !errors.Is(err, unix.ENOENT) {
		return fd, false, err
	}

	mkdirErr := unix.Mkdirat(parentFD, name, 0o777)
	if mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
		return -1, false, mkdirErr
	}
	created := mkdirErr == nil
	if created {
		flags |= unix.O_NOFOLLOW
	}

	fd, err = unix.Openat(parentFD, name, flags, 0)
	return fd, created, err
}

func openPrivateRootComponent(
	parentFD int,
	name string,
	path string,
	options DirectoryOptions,
	create bool,
) (int, bool, error) {
	flags := unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC
	pathFD, err := unix.Openat(parentFD, name, flags, 0)
	created := false
	if create && errors.Is(err, unix.ENOENT) {
		mkdirErr := unix.Mkdirat(parentFD, name, 0o700)
		if mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
			return -1, false, mkdirErr
		}
		created = mkdirErr == nil

		if created {
			flags |= unix.O_NOFOLLOW
		}
		pathFD, err = unix.Openat(parentFD, name, flags, 0)
	}
	if err != nil {
		return -1, created, err
	}
	defer closeDescriptor(options.Logger, pathFD)

	fd, err := retainPrivateRoot(pathFD, path, options)
	return fd, created, err
}

func retainPrivateRoot(
	pathFD int,
	path string,
	options DirectoryOptions,
) (int, error) {
	var pinned unix.Stat_t
	if err := unix.Fstat(pathFD, &pinned); err != nil {
		return -1, &fs.PathError{
			Op:   "stat private root",
			Path: path,
			Err:  err,
		}
	}
	if pinned.Mode&unix.S_IFMT != unix.S_IFDIR {
		return -1, fmt.Errorf("%w: %q is not a directory", ErrUnsafePath, path)
	}
	repairPrivateDirectoryMetadata(pathFD, path, &pinned, options)

	fd, err := unix.Openat(
		pathFD,
		".",
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return -1, err
	}
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil {
		closeDescriptor(options.Logger, fd)
		return -1, &fs.PathError{
			Op:   "stat opened private root",
			Path: path,
			Err:  err,
		}
	}
	if !sameDirectoryIdentity(&pinned, &opened) {
		closeDescriptor(options.Logger, fd)
		return -1, fmt.Errorf(
			"%w: private root %q changed while it was opened",
			ErrUnsafePath,
			path,
		)
	}
	return fd, nil
}

// OpenDirectoryFile retains and validates the exact directory referenced by
// file. diagnosticPath is used only in errors and Path; it is never opened or
// otherwise treated as authority.
func OpenDirectoryFile(
	file *os.File,
	diagnosticPath string,
	options DirectoryOptions,
) (*Directory, error) {
	if file == nil {
		return nil, fmt.Errorf("directory file must not be nil")
	}
	if diagnosticPath == "" {
		return nil, fmt.Errorf("directory diagnostic path must not be empty")
	}
	if err := validateDirectoryOptions(options); err != nil {
		return nil, err
	}

	raw, err := file.SyscallConn()
	if err != nil {
		return nil, fmt.Errorf("access directory file %q: %w", diagnosticPath, err)
	}

	fd := -1
	var duplicateErr error
	if err := raw.Control(func(source uintptr) {
		fd, duplicateErr = unix.FcntlInt(source, unix.F_DUPFD_CLOEXEC, 0)
	}); err != nil {
		return nil, fmt.Errorf("retain directory file %q: %w", diagnosticPath, err)
	}
	if duplicateErr != nil {
		return nil, fmt.Errorf("retain directory file %q: %w", diagnosticPath, duplicateErr)
	}

	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		closeDescriptor(options.Logger, fd)
		return nil, &fs.PathError{Op: "inspect directory file", Path: diagnosticPath, Err: err}
	}
	if flags&unix.O_PATH != 0 || flags&unix.O_ACCMODE != unix.O_RDONLY {
		closeDescriptor(options.Logger, fd)
		return nil, fmt.Errorf(
			"%w: directory file %q is not an ordinary read-only descriptor",
			ErrUnsafePath,
			diagnosticPath,
		)
	}
	if err := validateDirectory(fd, diagnosticPath); err != nil {
		closeDescriptor(options.Logger, fd)
		return nil, err
	}
	return newDirectory(fd, diagnosticPath, options), nil
}

// Close releases the retained directory capability.
func (d *Directory) Close() error {
	if d == nil || d.file == nil {
		return nil
	}

	file := d.file
	d.file = nil
	d.logger.DebugError(
		"close filesystem directory capability",
		file.Close(),
		"path", d.path,
	)
	return nil
}

// File returns a caller-owned CLOEXEC duplicate of the retained directory
// descriptor. The duplicate remains valid after Directory.Close.
func (d *Directory) File() (*os.File, error) {
	fd, err := d.duplicateFD()
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), d.path), nil
}

// Duplicate returns an independently closable directory capability for the
// same opened directory.
func (d *Directory) Duplicate() (*Directory, error) {
	fd, err := d.duplicateFD()
	if err != nil {
		return nil, err
	}
	return newDirectory(fd, d.path, d.options()), nil
}

// OpenDirectory opens an existing directory without following symbolic links.
func (d *Directory) OpenDirectory(name string) (*Directory, error) {
	if _, err := validateRelativePath(name); err != nil {
		return nil, err
	}

	rootFD, err := d.duplicateFD()
	if err != nil {
		return nil, err
	}
	defer closeDescriptor(d.logger, rootFD)

	fd, err := openRelative(
		rootFD,
		name,
		unix.O_RDONLY|unix.O_DIRECTORY,
		0,
		d.logger,
	)
	if err != nil {
		return nil, unsafeComponentError("open directory", name, err)
	}
	if err := validateDirectory(fd, name); err != nil {
		closeDescriptor(d.logger, fd)
		return nil, err
	}

	return newDirectory(fd, filepath.Join(d.path, name), d.options()), nil
}

// MkdirAll creates each missing component with mode 0700, fsyncs every
// modified parent, and returns a retained capability for the final directory.
// Existing components retain their filesystem ownership and mode; access
// failures are returned from the attempted traversal.
func (d *Directory) MkdirAll(name string) (*Directory, error) {
	components, err := validateRelativePath(name)
	if err != nil {
		return nil, err
	}

	currentFD, err := d.duplicateFD()
	if err != nil {
		return nil, err
	}

	currentPath := d.path
	for _, component := range components {
		componentPath := filepath.Join(currentPath, component)
		nextFD, openErr := openRelative(
			currentFD,
			component,
			unix.O_RDONLY|unix.O_DIRECTORY,
			0,
			d.logger,
		)
		created := false
		observedAfterMkdir := false
		if errors.Is(openErr, unix.ENOENT) {
			mkdirErr := unix.Mkdirat(currentFD, component, 0o700)
			if mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				closeDescriptor(d.logger, currentFD)
				return nil, &fs.PathError{Op: "mkdir", Path: filepath.Join(currentPath, component), Err: mkdirErr}
			}
			created = mkdirErr == nil
			observedAfterMkdir = true

			if created {
				nextFD, openErr = repairPrivateDirectory(
					currentFD,
					component,
					componentPath,
					d.options(),
				)
			} else {
				nextFD, openErr = openRelative(
					currentFD,
					component,
					unix.O_RDONLY|unix.O_DIRECTORY,
					0,
					d.logger,
				)
			}
		}
		if openErr != nil {
			closeDescriptor(d.logger, currentFD)
			return nil, unsafeComponentError("open directory", componentPath, openErr)
		}

		if err := validateDirectory(nextFD, componentPath); err != nil {
			closeDescriptor(d.logger, nextFD)
			closeDescriptor(d.logger, currentFD)
			return nil, err
		}
		if observedAfterMkdir {
			if err := unix.Fsync(currentFD); err != nil {
				closeDescriptor(d.logger, nextFD)
				closeDescriptor(d.logger, currentFD)
				return nil, &fs.PathError{Op: "sync directory", Path: currentPath, Err: err}
			}
		}

		closeDescriptor(d.logger, currentFD)
		currentFD = nextFD
		currentPath = componentPath
	}

	return newDirectory(currentFD, currentPath, d.options()), nil
}

// Sync commits directory-entry changes made through this capability.
func (d *Directory) Sync() error {
	fd, err := d.duplicateFD()
	if err != nil {
		return err
	}
	defer closeDescriptor(d.logger, fd)

	if err := unix.Fsync(fd); err != nil {
		return &fs.PathError{Op: "sync directory", Path: d.path, Err: err}
	}
	return nil
}

// RepairPrivateOwnershipAndMode best-effort corrects this Toby-owned
// directory's owner and mode. Failures emit a debug diagnostic and never fail the
// caller's filesystem operation.
func (d *Directory) RepairPrivateOwnershipAndMode() {
	if d == nil || d.file == nil {
		return
	}

	var stat unix.Stat_t
	if err := unix.Fstat(int(d.file.Fd()), &stat); err != nil {
		d.logger.DebugError(
			"inspect directory before correcting ownership and mode",
			err,
			"path",
			d.path,
		)
		return
	}
	repairPrivateDirectoryMetadata(
		int(d.file.Fd()),
		d.path,
		&stat,
		d.options(),
	)
}

func newDirectory(fd int, name string, options DirectoryOptions) *Directory {
	return &Directory{
		file:     os.NewFile(uintptr(fd), name),
		path:     name,
		ownerUID: options.OwnerUID,
		ownerGID: options.OwnerGID,
		logger:   options.Logger,
	}
}

func (d *Directory) options() DirectoryOptions {
	return DirectoryOptions{
		OwnerUID: d.ownerUID,
		OwnerGID: d.ownerGID,
		Logger:   d.logger,
	}
}

func (d *Directory) duplicateFD() (int, error) {
	if d == nil || d.file == nil {
		return -1, os.ErrInvalid
	}

	raw, err := d.file.SyscallConn()
	if err != nil {
		return -1, err
	}

	duplicate := -1
	var duplicateErr error
	if err := raw.Control(func(fd uintptr) {
		duplicate, duplicateErr = unix.FcntlInt(fd, unix.F_DUPFD_CLOEXEC, 0)
	}); err != nil {
		return -1, err
	}
	if duplicateErr != nil {
		return -1, duplicateErr
	}
	return duplicate, nil
}

func (d *Directory) openIndependent() (int, error) {
	if d == nil || d.file == nil {
		return -1, os.ErrInvalid
	}

	raw, err := d.file.SyscallConn()
	if err != nil {
		return -1, err
	}

	fd := -1
	var openErr error
	if err := raw.Control(func(source uintptr) {
		fd, openErr = unix.Openat(
			int(source),
			".",
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
			0,
		)
	}); err != nil {
		return -1, err
	}
	if openErr != nil {
		return -1, &fs.PathError{
			Op:   "reopen directory",
			Path: d.path,
			Err:  openErr,
		}
	}

	return fd, nil
}

func openRelative(
	dirFD int,
	name string,
	flags int,
	mode uint32,
	logger *diagnostic.Logger,
) (int, error) {
	if _, err := validateRelativePath(name); err != nil {
		return -1, err
	}

	how := &unix.OpenHow{
		Flags:   uint64(flags | unix.O_CLOEXEC | unix.O_NOFOLLOW),
		Mode:    uint64(mode),
		Resolve: openResolveFlags,
	}
	fd, err := unix.Openat2(dirFD, name, how)
	if err == nil {
		return fd, nil
	}
	if !openat2Unavailable(err) {
		return -1, err
	}
	return openAtWalk(dirFD, name, flags, mode, logger)
}

func openAtWalk(
	dirFD int,
	name string,
	flags int,
	mode uint32,
	logger *diagnostic.Logger,
) (int, error) {
	components, err := validateRelativePath(name)
	if err != nil {
		return -1, err
	}

	currentFD, err := unix.FcntlInt(uintptr(dirFD), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}

	for _, component := range components[:len(components)-1] {
		nextFD, openErr := unix.Openat(
			currentFD,
			component,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
			0,
		)
		closeDescriptor(logger, currentFD)
		if openErr != nil {
			return -1, openErr
		}
		currentFD = nextFD
	}

	fd, err := unix.Openat(
		currentFD,
		components[len(components)-1],
		flags|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		mode,
	)
	closeDescriptor(logger, currentFD)
	return fd, err
}

func openat2Unavailable(err error) bool {
	return errors.Is(err, unix.ENOSYS) ||
		errors.Is(err, unix.EINVAL) ||
		errors.Is(err, unix.E2BIG) ||
		errors.Is(err, unix.EOPNOTSUPP)
}

func repairPrivateDirectory(
	parentFD int,
	name string,
	path string,
	options DirectoryOptions,
) (int, error) {
	pathFD, err := openRelative(
		parentFD,
		name,
		unix.O_PATH|unix.O_DIRECTORY,
		0,
		options.Logger,
	)
	if err != nil {
		return -1, err
	}
	defer closeDescriptor(options.Logger, pathFD)

	var pinned unix.Stat_t
	if err := unix.Fstat(pathFD, &pinned); err != nil {
		return -1, &fs.PathError{Op: "stat private directory", Path: path, Err: err}
	}
	if pinned.Mode&unix.S_IFMT != unix.S_IFDIR {
		return -1, fmt.Errorf("%w: %q is not a directory", ErrUnsafePath, path)
	}
	repairPrivateDirectoryOwnership(pathFD, path, &pinned, options)
	if err := chmodPrivateDirectory(pathFD); err != nil {
		return -1, &fs.PathError{
			Op:   "set private directory mode",
			Path: path,
			Err:  err,
		}
	}

	if err := validatePrivateDirectoryName(parentFD, name, path, &pinned); err != nil {
		return -1, err
	}

	fd, err := openRelative(
		parentFD,
		name,
		unix.O_RDONLY|unix.O_DIRECTORY,
		0,
		options.Logger,
	)
	if err != nil {
		return -1, err
	}
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil {
		closeDescriptor(options.Logger, fd)
		return -1, &fs.PathError{Op: "stat opened private directory", Path: path, Err: err}
	}
	if !sameDirectoryIdentity(&pinned, &opened) {
		closeDescriptor(options.Logger, fd)
		return -1, fmt.Errorf("%w: repaired directory %q was replaced", ErrUnsafePath, path)
	}
	if err := validatePrivateDirectoryName(parentFD, name, path, &pinned); err != nil {
		closeDescriptor(options.Logger, fd)
		return -1, err
	}
	return fd, nil
}

func repairPrivateDirectoryMetadata(
	pathFD int,
	path string,
	stat *unix.Stat_t,
	options DirectoryOptions,
) {
	repairPrivateDirectoryOwnership(pathFD, path, stat, options)
	if stat.Mode&0o7777 != 0o700 {
		if err := chmodPrivateDirectory(pathFD); err != nil {
			options.Logger.DebugError(
				"correct Toby-owned directory mode",
				err,
				"path",
				path,
				"current_mode",
				fmt.Sprintf("%#o", stat.Mode&0o7777),
				"desired_mode",
				"0700",
			)
		}
	}
}

func repairPrivateDirectoryOwnership(
	pathFD int,
	path string,
	stat *unix.Stat_t,
	options DirectoryOptions,
) {
	if int(stat.Uid) != options.OwnerUID ||
		int(stat.Gid) != options.OwnerGID {
		if err := unix.Fchownat(
			pathFD,
			"",
			options.OwnerUID,
			options.OwnerGID,
			unix.AT_EMPTY_PATH,
		); err != nil {
			options.Logger.DebugError(
				"correct Toby-owned directory ownership",
				err,
				"path",
				path,
				"current_uid",
				stat.Uid,
				"current_gid",
				stat.Gid,
				"desired_uid",
				options.OwnerUID,
				"desired_gid",
				options.OwnerGID,
			)
		}
	}
}

func chmodPrivateDirectory(pathFD int) error {
	return unix.Fchmodat(pathFD, "", 0o700, unix.AT_EMPTY_PATH)
}

func validateDirectoryOptions(options DirectoryOptions) error {
	if options.OwnerUID < 0 {
		return fmt.Errorf(
			"%w: invalid owner UID %d",
			ErrUnsafePath,
			options.OwnerUID,
		)
	}
	if options.OwnerGID < 0 {
		return fmt.Errorf(
			"%w: invalid owner GID %d",
			ErrUnsafePath,
			options.OwnerGID,
		)
	}
	return nil
}

func validatePrivateDirectoryName(
	parentFD int,
	name string,
	path string,
	expected *unix.Stat_t,
) error {
	var actual unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &actual, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return &fs.PathError{Op: "stat private directory name", Path: path, Err: err}
	}
	if !sameDirectoryIdentity(expected, &actual) {
		return fmt.Errorf("%w: repaired directory %q was replaced", ErrUnsafePath, path)
	}
	return nil
}

func sameDirectoryIdentity(left, right *unix.Stat_t) bool {
	return left.Dev == right.Dev &&
		left.Ino == right.Ino &&
		left.Mode&unix.S_IFMT == unix.S_IFDIR &&
		right.Mode&unix.S_IFMT == unix.S_IFDIR
}

func validateDirectory(fd int, name string) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return &fs.PathError{Op: "stat directory", Path: name, Err: err}
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("%w: %q is not a directory", ErrUnsafePath, name)
	}
	return nil
}

func unsafeComponentError(op, name string, err error) error {
	if errors.Is(err, ErrUnsafePath) {
		return err
	}
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) || errors.Is(err, unix.EXDEV) {
		return fmt.Errorf("%w: %s %q: %v", ErrUnsafePath, op, name, err)
	}
	return &fs.PathError{Op: op, Path: name, Err: err}
}
