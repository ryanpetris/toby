//go:build linux

package oci

// Publishes immutable OCI objects and opens their rootfs directories as Linux
// descriptor capabilities.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/storage/safefs"
)

func (s *Store) publishObject(
	temporary string,
	object string,
) error {
	parent, err := s.root.MkdirAll(
		filepath.Dir(filepath.Join("objects", object)),
	)
	if err != nil {
		return fmt.Errorf("create OCI object parent: %w", err)
	}
	parent.RepairPrivateOwnershipAndMode()
	destination := s.objectPath(object)
	if err := os.Rename(temporary, destination); err != nil {
		s.logger.DebugError("close OCI object parent", parent.Close())
		return fmt.Errorf("publish OCI object: %w", err)
	}
	if err := parent.Sync(); err != nil {
		s.logger.DebugError("close OCI object parent", parent.Close())
		return fmt.Errorf("sync published OCI object parent: %w", err)
	}
	s.logger.DebugError("close published OCI object parent", parent.Close())

	return nil
}

func (s *Store) openObject(
	path string,
	expected Metadata,
) (*Prepared, bool, error) {
	metadata, err := s.readObjectMetadata(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf(
			"read cached OCI object %q: %w",
			path,
			err,
		)
	}
	if expected.Spec.Manifest.Digest != "" &&
		!reflect.DeepEqual(metadata.Spec, expected.Spec) {
		return nil, false, fmt.Errorf(
			"cached OCI object metadata does not match pulled image",
		)
	}
	if expected.Spec.Manifest.Digest == "" {
		expected.Spec = metadata.Spec
	}

	rootfsPath := filepath.Join(path, "bundle", "rootfs")
	fd, err := unix.Open(
		rootfsPath,
		unix.O_RDONLY|
			unix.O_DIRECTORY|
			unix.O_NOFOLLOW|
			unix.O_CLOEXEC,
		0,
	)
	if errors.Is(err, unix.ENOENT) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, &fs.PathError{
			Op:   "open cached OCI rootfs",
			Path: rootfsPath,
			Err:  err,
		}
	}

	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		closeDescriptor(s.logger, fd)
		return nil, false, &fs.PathError{
			Op:   "stat cached OCI rootfs",
			Path: rootfsPath,
			Err:  err,
		}
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		closeDescriptor(s.logger, fd)
		return nil, false, fmt.Errorf(
			"cached OCI rootfs %q is not a directory",
			rootfsPath,
		)
	}

	return newPrepared(
		expected,
		rootfsPath,
		os.NewFile(uintptr(fd), rootfsPath),
		nil,
		s.logger,
	), true, nil
}

func (s *Store) retainObject(
	ctx context.Context,
	object string,
	expected Metadata,
) (*Prepared, bool, error) {
	lease, err := s.lockContextMode(
		ctx,
		objectLockName(object),
		safefs.LockShared,
	)
	if err != nil {
		return nil, false, fmt.Errorf(
			"retain OCI object %q: %w",
			object,
			err,
		)
	}

	prepared, found, err := s.openObject(
		s.objectPath(object),
		expected,
	)
	if err != nil || !found {
		s.logger.DebugError("release OCI object lease", lease.Close())
		return prepared, found, err
	}
	prepared.lease = lease
	return prepared, true, nil
}
