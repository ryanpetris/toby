//go:build linux

package runtimeassets

// Publishes private Linux materializations and retains exact regular-file
// descriptors independent of their diagnostic host paths.

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/sandbox/bwrap"
	"petris.dev/toby/internal/sandbox/mount"
	"petris.dev/toby/internal/storage/safefs"
)

const (
	materializationAttempts    = 16
	materializationRandomBytes = 16
	cleanupEntrySlack          = 1024
)

// Materialize publishes every registered asset beneath one unique private
// directory. root is a caller-owned capability and remains open.
func (r *Registry) Materialize(
	root *safefs.Directory,
	logger *diagnostic.Logger,
) (*Set, error) {
	if r == nil {
		return nil, fmt.Errorf("runtime asset registry must not be nil")
	}
	if err := validateRoot(root, logger); err != nil {
		return nil, err
	}

	parent, err := root.Duplicate()
	if err != nil {
		return nil, fmt.Errorf("retain runtime asset root: %w", err)
	}

	for range materializationAttempts {
		storageName, err := newStorageName()
		if err != nil {
			logger.DebugError(
				"close runtime asset root after name generation failed",
				parent.Close(),
			)
			return nil, err
		}

		sources := make(map[string]*os.File, len(r.assets))
		published, err := parent.PublishDirectory(
			storageName,
			uint64(len(r.assets))+cleanupEntrySlack,
			func(stage *safefs.Directory) error {
				for index, asset := range r.assets {
					name := assetFileName(index)
					if err := stage.WriteFile(name, asset.Data, asset.Mode); err != nil {
						return fmt.Errorf(
							"write runtime asset %q: %w",
							asset.Target,
							err,
						)
					}

					source, err := stage.OpenFile(name)
					if err != nil {
						return fmt.Errorf(
							"open runtime asset %q: %w",
							asset.Target,
							err,
						)
					}
					if err := validateSource(
						source,
						asset.Target,
						int64(len(asset.Data)),
					); err != nil {
						logger.DebugError(
							"close invalid runtime asset source",
							source.Close(),
							"target",
							asset.Target,
						)
						return err
					}
					sources[asset.Target] = source
				}

				return nil
			},
		)
		if err != nil {
			closeErr := closeSources(sources)
			logger.DebugError(
				"close runtime asset sources after publication failed",
				closeErr,
			)
			if published {
				logger.DebugError(
					"remove published runtime assets after publication failed",
					parent.RemoveAllOwned(
						storageName,
						uint64(len(r.assets))+
							cleanupEntrySlack,
					),
					"storage_name", storageName,
				)
			}
			logger.DebugError(
				"close runtime asset root after publication failed",
				parent.Close(),
			)
			return nil, fmt.Errorf("publish runtime assets: %w", err)
		}
		if !published {
			logger.DebugError(
				"close unpublished runtime asset sources",
				closeSources(sources),
				"storage_name", storageName,
			)
			continue
		}

		assets := make([]bwrap.RuntimeAsset, len(r.assets))
		for index, asset := range r.assets {
			assets[index] = bwrap.RuntimeAsset{
				HostPath: filepath.Join(
					parent.Path(),
					storageName,
					assetFileName(index),
				),
				Target: asset.Target,
				Access: mount.AccessReadOnly,
			}
		}

		return &Set{
			assets:         assets,
			sources:        sources,
			parent:         parent,
			storageName:    storageName,
			cleanupEntries: uint64(len(r.assets)) + cleanupEntrySlack,
			logger:         logger,
		}, nil
	}

	logger.DebugError(
		"close runtime asset root after allocation failed",
		parent.Close(),
	)
	return nil, fmt.Errorf(
		"allocate runtime asset storage: exhausted %d collision attempts",
		materializationAttempts,
	)
}

func validateRoot(
	root *safefs.Directory,
	logger *diagnostic.Logger,
) error {
	if root == nil {
		return fmt.Errorf("runtime asset root must not be nil")
	}
	if root.Path() == "" ||
		!filepath.IsAbs(root.Path()) ||
		filepath.Clean(root.Path()) != root.Path() ||
		strings.ContainsRune(root.Path(), 0) {
		return fmt.Errorf(
			"runtime asset root diagnostic path must be clean and absolute: %q",
			root.Path(),
		)
	}

	file, err := root.File()
	if err != nil {
		return fmt.Errorf("inspect runtime asset root: %w", err)
	}
	defer func() {
		logger.DebugError(
			"close runtime asset root descriptor",
			file.Close(),
		)
	}()

	var status unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &status); err != nil {
		return fmt.Errorf("stat runtime asset root: %w", err)
	}
	if status.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("runtime asset root is not a directory")
	}

	return nil
}

func validateSource(
	source *os.File,
	target string,
	size int64,
) error {
	flags, err := unix.FcntlInt(source.Fd(), unix.F_GETFL, 0)
	if err != nil {
		return fmt.Errorf("inspect runtime asset %q descriptor: %w", target, err)
	}
	if flags&unix.O_PATH != 0 || flags&unix.O_ACCMODE != unix.O_RDONLY {
		return fmt.Errorf(
			"runtime asset %q source is not an ordinary read-only descriptor",
			target,
		)
	}

	var status unix.Stat_t
	if err := unix.Fstat(int(source.Fd()), &status); err != nil {
		return fmt.Errorf("stat runtime asset %q: %w", target, err)
	}
	if status.Mode&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf("runtime asset %q source is not a regular file", target)
	}
	if status.Size != size {
		return fmt.Errorf(
			"runtime asset %q source has size %d, want %d",
			target,
			status.Size,
			size,
		)
	}

	return nil
}

func assetFileName(index int) string {
	return fmt.Sprintf("asset-%06d", index)
}

func newStorageName() (string, error) {
	var random [materializationRandomBytes]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate runtime asset storage name: %w", err)
	}

	return ".assets-" + hex.EncodeToString(random[:]), nil
}

func closeSources(sources map[string]*os.File) error {
	var closeErr error
	for target, source := range sources {
		if source != nil {
			closeErr = errors.Join(closeErr, source.Close())
		}
		delete(sources, target)
	}

	return closeErr
}
