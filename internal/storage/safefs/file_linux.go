//go:build linux

package safefs

// Reads and writes owned regular files relative to directory capabilities.

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

const regularOpenAttempts = 8

// OpenFile opens an owned regular file without following symbolic links. The
// caller owns the returned read-only file and must close it.
func (d *Directory) OpenFile(name string) (*os.File, error) {
	parentFD, base, path, err := d.openParent(name)
	if err != nil {
		return nil, err
	}
	defer closeDescriptor(d.logger, parentFD)

	fd, err := openExistingRegular(
		parentFD,
		base,
		unix.O_RDONLY,
		path,
		d.logger,
	)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

// ReadFile reads an owned regular file without following symbolic links. It
// returns ErrLimitExceeded before returning more than maxBytes.
func (d *Directory) ReadFile(name string, maxBytes int64) ([]byte, error) {
	if maxBytes < 0 {
		return nil, fmt.Errorf("read limit must not be negative")
	}

	file, err := d.OpenFile(name)
	if err != nil {
		return nil, err
	}
	defer closeFile(d.logger, file)

	info, err := file.Stat()
	if err != nil {
		return nil, &fs.PathError{Op: "stat file", Path: file.Name(), Err: err}
	}
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("%w: file %q exceeds %d bytes", ErrLimitExceeded, file.Name(), maxBytes)
	}

	data, err := io.ReadAll(io.LimitReader(file, maxBytes))
	if err != nil {
		return nil, &fs.PathError{Op: "read file", Path: file.Name(), Err: err}
	}

	var extra [1]byte
	count, err := file.Read(extra[:])
	if count != 0 {
		return nil, fmt.Errorf("%w: file %q exceeds %d bytes", ErrLimitExceeded, file.Name(), maxBytes)
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, &fs.PathError{Op: "read file", Path: file.Name(), Err: err}
	}
	return data, nil
}

// WriteFile creates or truncates an owned regular file without following
// symbolic links, applies mode exactly, and fsyncs the file. Call PublishFile
// when readers must not observe an in-place update.
func (d *Directory) WriteFile(name string, data []byte, mode fs.FileMode) error {
	permissions, err := regularPermissions(mode)
	if err != nil {
		return err
	}

	parentFD, base, path, err := d.openParent(name)
	if err != nil {
		return err
	}
	defer closeDescriptor(d.logger, parentFD)

	fd, created, err := openWritableRegular(
		parentFD,
		base,
		path,
		d.logger,
	)
	if err != nil {
		return err
	}

	if err := writeFileDescriptor(fd, data, permissions, path); err != nil {
		closeDescriptor(d.logger, fd)
		if created {
			d.logger.DebugError(
				"remove incomplete created file",
				removeCreatedFile(parentFD, base, path),
				"path", path,
			)
		}
		return err
	}
	if err := unix.Close(fd); err != nil {
		d.logger.DebugError(
			"close written file",
			&fs.PathError{Op: "close file", Path: path, Err: err},
			"path", path,
		)
	}

	if created {
		if err := unix.Fsync(parentFD); err != nil {
			return &fs.PathError{Op: "sync directory", Path: filepath.Dir(path), Err: err}
		}
	}
	return nil
}

func (d *Directory) openParent(name string) (int, string, string, error) {
	parent, base, err := splitParent(name)
	if err != nil {
		return -1, "", "", err
	}

	rootFD, err := d.duplicateFD()
	if err != nil {
		return -1, "", "", err
	}

	path := filepath.Join(d.path, name)
	if parent == "" {
		return rootFD, base, path, nil
	}

	parentFD, err := openRelative(
		rootFD,
		parent,
		unix.O_RDONLY|unix.O_DIRECTORY,
		0,
		d.logger,
	)
	closeDescriptor(d.logger, rootFD)
	if err != nil {
		return -1, "", "", unsafeComponentError("open parent directory", filepath.Join(d.path, parent), err)
	}
	if err := validateDirectory(parentFD, filepath.Join(d.path, parent)); err != nil {
		closeDescriptor(d.logger, parentFD)
		return -1, "", "", err
	}
	return parentFD, base, path, nil
}

func openExistingRegular(
	parentFD int,
	base string,
	flags int,
	path string,
	logger *diagnostic.Logger,
) (int, error) {
	for range regularOpenAttempts {
		pathFD, err := openRelative(
			parentFD,
			base,
			unix.O_PATH,
			0,
			logger,
		)
		if err != nil {
			return -1, unsafeComponentError("open file", path, err)
		}

		var expected unix.Stat_t
		if err := unix.Fstat(pathFD, &expected); err != nil {
			closeDescriptor(logger, pathFD)
			return -1, &fs.PathError{Op: "stat file", Path: path, Err: err}
		}
		if err := validateRegularStat(&expected, path); err != nil {
			closeDescriptor(logger, pathFD)
			return -1, err
		}

		fd, err := openRelative(
			parentFD,
			base,
			flags|unix.O_NONBLOCK,
			0,
			logger,
		)
		if err != nil {
			closeDescriptor(logger, pathFD)
			if errors.Is(err, unix.ENOENT) {
				continue
			}
			return -1, unsafeComponentError("open file", path, err)
		}

		var actual unix.Stat_t
		statErr := unix.Fstat(fd, &actual)
		closeDescriptor(logger, pathFD)
		if statErr != nil {
			closeDescriptor(logger, fd)
			return -1, &fs.PathError{Op: "stat file", Path: path, Err: statErr}
		}
		if expected.Dev == actual.Dev && expected.Ino == actual.Ino {
			if err := validateRegularStat(&actual, path); err != nil {
				closeDescriptor(logger, fd)
				return -1, err
			}
			return fd, nil
		}
		closeDescriptor(logger, fd)
	}

	return -1, fmt.Errorf("%w: file %q changed while it was opened", ErrUnsafePath, path)
}

func openWritableRegular(
	parentFD int,
	base string,
	path string,
	logger *diagnostic.Logger,
) (int, bool, error) {
	for range regularOpenAttempts {
		fd, err := openExistingRegular(
			parentFD,
			base,
			unix.O_WRONLY,
			path,
			logger,
		)
		if err == nil {
			return fd, false, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return -1, false, err
		}

		fd, err = openRelative(
			parentFD,
			base,
			unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NONBLOCK,
			0o600,
			logger,
		)
		if err == nil {
			var stat unix.Stat_t
			if statErr := unix.Fstat(fd, &stat); statErr != nil {
				closeDescriptor(logger, fd)
				return -1, false, &fs.PathError{Op: "stat file", Path: path, Err: statErr}
			}
			if statErr := validateRegularStat(&stat, path); statErr != nil {
				closeDescriptor(logger, fd)
				logger.DebugError(
					"remove invalid created file",
					removeCreatedFile(parentFD, base, path),
					"path", path,
				)
				return -1, false, statErr
			}
			return fd, true, nil
		}
		if !errors.Is(err, unix.EEXIST) {
			return -1, false, &fs.PathError{Op: "create file", Path: path, Err: err}
		}
	}

	return -1, false, fmt.Errorf("%w: file %q changed while it was created", ErrUnsafePath, path)
}

func validateRegularStat(stat *unix.Stat_t, path string) error {
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf("%w: %q is not a regular file", ErrUnsafePath, path)
	}
	return nil
}

func regularPermissions(mode fs.FileMode) (uint32, error) {
	if mode&^fs.ModePerm != 0 {
		return 0, fmt.Errorf("file mode %v contains non-permission bits", mode)
	}
	return uint32(mode.Perm()), nil
}

func writeFileDescriptor(fd int, data []byte, mode uint32, path string) error {
	if err := unix.Fchmod(fd, mode); err != nil {
		return &fs.PathError{Op: "set file mode", Path: path, Err: err}
	}
	if err := unix.Ftruncate(fd, 0); err != nil {
		return &fs.PathError{Op: "truncate file", Path: path, Err: err}
	}

	for len(data) > 0 {
		written, err := unix.Write(fd, data)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return &fs.PathError{Op: "write file", Path: path, Err: err}
		}
		if written == 0 {
			return &fs.PathError{Op: "write file", Path: path, Err: io.ErrShortWrite}
		}
		data = data[written:]
	}

	if err := unix.Fsync(fd); err != nil {
		return &fs.PathError{Op: "sync file", Path: path, Err: err}
	}
	return nil
}

func removeCreatedFile(parentFD int, base, path string) error {
	if err := unix.Unlinkat(parentFD, base, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return &fs.PathError{Op: "remove incomplete file", Path: path, Err: err}
	}
	if err := unix.Fsync(parentFD); err != nil {
		return &fs.PathError{Op: "sync directory", Path: filepath.Dir(path), Err: err}
	}
	return nil
}
