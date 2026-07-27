//go:build linux

package storage

// Copies immutable seed content through directory descriptors without
// following archive-created or concurrently substituted symbolic links.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/storage/safefs"
)

type seedInode struct {
	device uint64
	inode  uint64
}

type seedHardlink struct {
	path      string
	linkCount uint64
	remaining uint64
}

type seedCopyState struct {
	limits                Limits
	uid                   int
	gid                   int
	logger                *diagnostic.Logger
	entries               uint64
	bytes                 uint64
	hardlinkMetadataBytes int64
	sourceDev             uint64
	destRoot              int
	hardlinks             map[seedInode]seedHardlink
}

func copySeedDirectory(
	ctx context.Context,
	source *os.File,
	destination *safefs.Directory,
	uid, gid int,
	limits Limits,
	logger *diagnostic.Logger,
) (returnErr error) {
	destinationFile, err := destination.File()
	if err != nil {
		return err
	}
	defer func() {
		logger.DebugError(
			"close seed destination directory",
			destinationFile.Close(),
		)
	}()
	var sourceStat unix.Stat_t
	if err := unix.Fstat(int(source.Fd()), &sourceStat); err != nil {
		return fmt.Errorf("stat immutable seed root: %w", err)
	}

	state := &seedCopyState{
		limits:    limits,
		uid:       uid,
		gid:       gid,
		logger:    logger,
		sourceDev: uint64(sourceStat.Dev),
		destRoot:  int(destinationFile.Fd()),
		hardlinks: make(map[seedInode]seedHardlink),
	}
	if err := state.copyDirectory(
		ctx,
		int(source.Fd()),
		int(destinationFile.Fd()),
		"",
		0,
	); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := unix.Fsync(int(destinationFile.Fd())); err != nil {
		return fmt.Errorf("sync seeded directory: %w", err)
	}
	return nil
}

func (s *seedCopyState) copyDirectory(
	ctx context.Context,
	sourceFD, destinationFD int,
	relative string,
	depth int,
) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if depth > s.limits.Depth {
		return fmt.Errorf("%w: seed depth exceeds %d", ErrSeedLimitExceeded, s.limits.Depth)
	}

	duplicate, err := unix.Openat(
		sourceFD,
		".",
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return err
	}
	directory := os.NewFile(uintptr(duplicate), relative)
	defer func() {
		s.logger.DebugError(
			"close seed source directory",
			directory.Close(),
		)
	}()

	for {
		entries, readErr := directory.ReadDir(128)
		for _, entry := range entries {
			if err := s.copyEntry(
				ctx,
				sourceFD,
				destinationFD,
				relative,
				depth,
				entry.Name(),
			); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}

	return nil
}

func (s *seedCopyState) copyEntry(
	ctx context.Context,
	sourceFD, destinationFD int,
	relative string,
	depth int,
	name string,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSeedComponent(name); err != nil {
		return err
	}
	childRelative := name
	if relative != "" {
		childRelative = relative + "/" + name
	}
	if len(childRelative) > s.limits.PathBytes {
		return fmt.Errorf("%w: seed path exceeds %d bytes", ErrSeedLimitExceeded, s.limits.PathBytes)
	}
	if s.entries >= s.limits.SeedEntries {
		return fmt.Errorf("%w: seed entries exceed %d", ErrSeedLimitExceeded, s.limits.SeedEntries)
	}
	s.entries++

	var stat unix.Stat_t
	if err := unix.Fstatat(sourceFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return &fs.PathError{Op: "stat seed", Path: childRelative, Err: err}
	}
	if uint64(stat.Dev) != s.sourceDev {
		return fmt.Errorf(
			"%w: seed path %q crosses a filesystem boundary",
			ErrUnsupportedSeed,
			childRelative,
		)
	}
	entryFD, err := openSeedPath(
		sourceFD,
		name,
		childRelative,
		unix.O_PATH,
	)
	if err != nil {
		return err
	}
	if err := verifySeedInode(entryFD, &stat, childRelative); err != nil {
		closeDescriptor(s.logger, entryFD)
		return err
	}
	if err := unix.Close(entryFD); err != nil {
		return &fs.PathError{Op: "close immutable seed entry", Path: childRelative, Err: err}
	}
	switch stat.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		if err := s.copyChildDirectory(
			ctx,
			sourceFD,
			destinationFD,
			name,
			childRelative,
			&stat,
			depth,
		); err != nil {
			return err
		}
	case unix.S_IFREG:
		if err := s.copyRegular(
			ctx,
			sourceFD,
			destinationFD,
			name,
			childRelative,
			&stat,
		); err != nil {
			return err
		}
	case unix.S_IFLNK:
		if err := s.copySymlink(sourceFD, destinationFD, name, childRelative, &stat); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: seed path %q has mode %o", ErrUnsupportedSeed, childRelative, stat.Mode)
	}
	return nil
}

func (s *seedCopyState) copyChildDirectory(
	ctx context.Context,
	sourceParent, destinationParent int,
	name, relative string,
	stat *unix.Stat_t,
	depth int,
) (returnErr error) {
	source, err := openSeedPath(
		sourceParent,
		name,
		relative,
		unix.O_RDONLY|unix.O_DIRECTORY,
	)
	if err != nil {
		return err
	}
	defer func() {
		s.logger.DebugError(
			"close seed source directory descriptor",
			unix.Close(source),
			"path",
			relative,
		)
	}()
	if err := verifySeedInode(source, stat, relative); err != nil {
		return err
	}

	if err := unix.Mkdirat(destinationParent, name, 0o700); err != nil {
		return &fs.PathError{Op: "create seeded directory", Path: relative, Err: err}
	}
	destination, err := s.openCreatedSeedDirectory(
		destinationParent,
		name,
		relative,
	)
	if err != nil {
		return err
	}
	defer func() {
		s.logger.DebugError(
			"close seed destination directory descriptor",
			unix.Close(destination),
			"path",
			relative,
		)
	}()

	if err := s.copyDirectory(ctx, source, destination, relative, depth+1); err != nil {
		return err
	}
	if err := unix.Fchown(destination, s.uid, s.gid); err != nil {
		s.logger.DebugError(
			"set ownership of seeded directory",
			err,
			"path",
			relative,
			"uid",
			s.uid,
			"gid",
			s.gid,
		)
	}
	if err := unix.Fchmod(destination, seedMode(stat.Mode)); err != nil {
		return &fs.PathError{
			Op:   "set seeded directory mode",
			Path: relative,
			Err:  err,
		}
	}
	if err := unix.Fsync(destination); err != nil {
		return &fs.PathError{Op: "sync seeded directory", Path: relative, Err: err}
	}
	if err := unix.Fsync(destinationParent); err != nil {
		return &fs.PathError{Op: "sync seed parent", Path: filepath.Dir(relative), Err: err}
	}
	return nil
}

func (s *seedCopyState) openCreatedSeedDirectory(
	parentFD int,
	name string,
	relative string,
) (destination int, returnErr error) {
	pathFD, err := unix.Openat(
		parentFD,
		name,
		unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return -1, &fs.PathError{Op: "pin seeded directory", Path: relative, Err: err}
	}
	defer func() {
		s.logger.DebugError(
			"close pinned seed directory descriptor",
			unix.Close(pathFD),
			"path",
			relative,
		)
	}()

	if err := unix.Fchmodat(
		pathFD,
		"",
		0o700,
		unix.AT_EMPTY_PATH,
	); err != nil {
		return -1, &fs.PathError{
			Op:   "make seeded directory accessible",
			Path: relative,
			Err:  err,
		}
	}

	var pinned unix.Stat_t
	if err := unix.Fstat(pathFD, &pinned); err != nil {
		return -1, &fs.PathError{Op: "stat seeded directory", Path: relative, Err: err}
	}
	if pinned.Mode&unix.S_IFMT != unix.S_IFDIR {
		return -1, fmt.Errorf("seeded directory %q changed type", relative)
	}

	destination, err = unix.Openat(
		pathFD,
		".",
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return -1, &fs.PathError{Op: "open seeded directory", Path: relative, Err: err}
	}

	var opened unix.Stat_t
	if err := unix.Fstat(destination, &opened); err != nil {
		s.logger.DebugError(
			"close invalid seeded directory descriptor",
			unix.Close(destination),
		)
		return -1, &fs.PathError{
			Op:   "stat opened seeded directory",
			Path: relative,
			Err:  err,
		}
	}
	if pinned.Dev != opened.Dev ||
		pinned.Ino != opened.Ino ||
		opened.Mode&unix.S_IFMT != unix.S_IFDIR {
		s.logger.DebugError(
			"close changed seeded directory descriptor",
			unix.Close(destination),
		)
		return -1, fmt.Errorf(
			"seeded directory %q changed while it was reopened",
			relative,
		)
	}
	return destination, nil
}

func (s *seedCopyState) copyRegular(
	ctx context.Context,
	sourceParent, destinationParent int,
	name, relative string,
	stat *unix.Stat_t,
) error {
	if stat.Size < 0 {
		return fmt.Errorf("%w: seed file %q has a negative size", ErrUnsupportedSeed, relative)
	}
	source, err := openSeedPath(
		sourceParent,
		name,
		relative,
		unix.O_RDONLY|unix.O_NONBLOCK,
	)
	if err != nil {
		return err
	}
	if err := verifySeedInode(source, stat, relative); err != nil {
		closeDescriptor(s.logger, source)
		return err
	}

	key := seedInode{device: uint64(stat.Dev), inode: stat.Ino}
	if existing, ok := s.hardlinks[key]; ok {
		if uint64(stat.Nlink) != existing.linkCount {
			closeDescriptor(s.logger, source)
			return fmt.Errorf("%w: seed hard-link count changed for %q", ErrUnsupportedSeed, relative)
		}
		s.logger.DebugError(
			"close seed hard-link source",
			unix.Close(source),
			"path",
			relative,
		)
		if err := unix.Linkat(s.destRoot, existing.path, destinationParent, name, 0); err != nil {
			return &fs.PathError{Op: "link seeded file", Path: relative, Err: err}
		}
		if err := unix.Fsync(destinationParent); err != nil {
			return &fs.PathError{Op: "sync seed parent", Path: filepath.Dir(relative), Err: err}
		}
		existing.remaining--
		if existing.remaining == 0 {
			delete(s.hardlinks, key)
			s.hardlinkMetadataBytes -= int64(len(existing.path))
		} else {
			s.hardlinks[key] = existing
		}
		return nil
	}
	if err := s.reserveBytes(uint64(stat.Size)); err != nil {
		closeDescriptor(s.logger, source)
		return err
	}

	destination, err := unix.Openat(
		destinationParent,
		name,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		closeDescriptor(s.logger, source)
		return &fs.PathError{Op: "create seeded file", Path: relative, Err: err}
	}

	sourceFile := os.NewFile(uintptr(source), relative)
	destinationFile := os.NewFile(uintptr(destination), relative)
	reader := io.LimitReader(
		seedReaderFunc(func(data []byte) (int, error) {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			return sourceFile.Read(data)
		}),
		stat.Size+1,
	)
	written, copyErr := io.Copy(destinationFile, reader)
	if copyErr == nil && written != stat.Size {
		copyErr = fmt.Errorf("seed file changed size: copied %d bytes, expected %d", written, stat.Size)
	}
	if copyErr == nil {
		copyErr = verifySeedInode(int(sourceFile.Fd()), stat, relative)
	}
	if copyErr == nil {
		if err := destinationFile.Chown(s.uid, s.gid); err != nil {
			s.logger.DebugError(
				"set ownership of seeded file",
				err,
				"path",
				relative,
				"uid",
				s.uid,
				"gid",
				s.gid,
			)
		}
		if err := destinationFile.Chmod(
			os.FileMode(seedMode(stat.Mode)),
		); err != nil {
			copyErr = fmt.Errorf(
				"set seeded file mode: %w",
				err,
			)
		}
	}
	if copyErr == nil {
		copyErr = destinationFile.Sync()
	}
	s.logger.DebugError(
		"close seed source file",
		sourceFile.Close(),
		"path", relative,
	)
	s.logger.DebugError(
		"close seed destination file",
		destinationFile.Close(),
		"path", relative,
	)
	if copyErr != nil {
		return &fs.PathError{
			Op:   "copy seeded file",
			Path: relative,
			Err:  copyErr,
		}
	}
	if err := unix.Fsync(destinationParent); err != nil {
		return &fs.PathError{Op: "sync seed parent", Path: filepath.Dir(relative), Err: err}
	}
	if stat.Nlink > 1 {
		pathBytes := int64(len(relative))
		if pathBytes > s.limits.MetadataSize-s.hardlinkMetadataBytes {
			return fmt.Errorf(
				"%w: seed hard-link metadata exceeds %d bytes",
				ErrSeedLimitExceeded,
				s.limits.MetadataSize,
			)
		}
		s.hardlinkMetadataBytes += pathBytes
		s.hardlinks[key] = seedHardlink{
			path:      relative,
			linkCount: uint64(stat.Nlink),
			remaining: uint64(stat.Nlink) - 1,
		}
	}
	return nil
}

func openSeedPath(parentFD int, name, relative string, flags uint64) (int, error) {
	fd, err := unix.Openat2(parentFD, name, &unix.OpenHow{
		Flags: flags | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Resolve: unix.RESOLVE_BENEATH |
			unix.RESOLVE_NO_MAGICLINKS |
			unix.RESOLVE_NO_SYMLINKS |
			unix.RESOLVE_NO_XDEV,
	})
	if err == nil {
		return fd, nil
	}
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) {
		return -1, fmt.Errorf("secure immutable seed traversal is unsupported: %w", err)
	}
	if errors.Is(err, unix.EXDEV) || errors.Is(err, unix.ELOOP) {
		return -1, fmt.Errorf(
			"%w: seed path %q crosses an immutable-tree boundary",
			ErrUnsupportedSeed,
			relative,
		)
	}
	return -1, &fs.PathError{Op: "open immutable seed path", Path: relative, Err: err}
}

func (s *seedCopyState) copySymlink(
	sourceParent, destinationParent int,
	name, relative string,
	expected *unix.Stat_t,
) error {
	buffer := make([]byte, s.limits.PathBytes+1)
	count, err := unix.Readlinkat(sourceParent, name, buffer)
	if err != nil {
		return &fs.PathError{Op: "read seed symlink", Path: relative, Err: err}
	}
	if count == len(buffer) {
		return fmt.Errorf("%w: seed symlink target exceeds %d bytes", ErrSeedLimitExceeded, s.limits.PathBytes)
	}
	target := string(buffer[:count])
	if err := validateSeedSymlink(relative, target); err != nil {
		return err
	}
	if err := verifySeedPath(sourceParent, name, expected, relative); err != nil {
		return err
	}
	if err := s.reserveBytes(uint64(count)); err != nil {
		return err
	}
	if err := unix.Symlinkat(target, destinationParent, name); err != nil {
		return &fs.PathError{Op: "create seeded symlink", Path: relative, Err: err}
	}
	if err := unix.Fchownat(
		destinationParent,
		name,
		s.uid,
		s.gid,
		unix.AT_SYMLINK_NOFOLLOW,
	); err != nil {
		s.logger.DebugError(
			"set ownership of seeded symlink",
			err,
			"path",
			relative,
			"uid",
			s.uid,
			"gid",
			s.gid,
		)
	}
	if err := unix.Fsync(destinationParent); err != nil {
		return &fs.PathError{Op: "sync seed parent", Path: filepath.Dir(relative), Err: err}
	}
	return nil
}

func (s *seedCopyState) reserveBytes(size uint64) error {
	if size > s.limits.SeedBytes-s.bytes {
		return fmt.Errorf("%w: seed bytes exceed %d", ErrSeedLimitExceeded, s.limits.SeedBytes)
	}
	s.bytes += size
	return nil
}

func verifySeedInode(fd int, expected *unix.Stat_t, relative string) error {
	var actual unix.Stat_t
	if err := unix.Fstat(fd, &actual); err != nil {
		return &fs.PathError{Op: "stat opened seed", Path: relative, Err: err}
	}
	if actual.Dev != expected.Dev || actual.Ino != expected.Ino ||
		actual.Mode&unix.S_IFMT != expected.Mode&unix.S_IFMT ||
		actual.Size != expected.Size ||
		actual.Nlink != expected.Nlink {
		return fmt.Errorf("seed path %q changed while it was opened", relative)
	}
	return nil
}

func verifySeedPath(parentFD int, name string, expected *unix.Stat_t, relative string) error {
	var actual unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &actual, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return &fs.PathError{Op: "restat seed", Path: relative, Err: err}
	}
	if actual.Dev != expected.Dev || actual.Ino != expected.Ino ||
		actual.Mode&unix.S_IFMT != expected.Mode&unix.S_IFMT ||
		actual.Size != expected.Size ||
		actual.Nlink != expected.Nlink {
		return fmt.Errorf("seed path %q changed while it was read", relative)
	}
	return nil
}

func validateSeedComponent(value string) error {
	if value == "" || value == "." || value == ".." ||
		len(value) > 255 || strings.ContainsAny(value, "/\x00") {
		return fmt.Errorf("%w: invalid seed path component %q", ErrUnsupportedSeed, value)
	}
	return nil
}

func validateSeedSymlink(relative, target string) error {
	if target == "" || path.IsAbs(target) || path.Clean(target) != target ||
		strings.ContainsRune(target, 0) {
		return fmt.Errorf("%w: unsafe seed symlink %q -> %q", ErrUnsupportedSeed, relative, target)
	}
	resolved := path.Clean(path.Join(path.Dir(relative), target))
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return fmt.Errorf("%w: escaping seed symlink %q -> %q", ErrUnsupportedSeed, relative, target)
	}
	return nil
}

func seedMode(mode uint32) uint32 {
	return mode & 0o7777
}

type seedReaderFunc func([]byte) (int, error)

var _ io.Reader = seedReaderFunc(nil)

func (f seedReaderFunc) Read(data []byte) (int, error) {
	return f(data)
}
