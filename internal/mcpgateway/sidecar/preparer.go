package sidecar

// Prepares exact OCI and mount capabilities and starts each process with a
// fresh transient overlay.

import (
	"context"
	"fmt"
	"os"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/mcpgateway"
	"petris.dev/toby/internal/sandbox/bwrap"
	"petris.dev/toby/internal/sandbox/mount"
)

// Preparer owns the reusable OCI, run-storage, and Bubblewrap process
// dependencies for local MCP sidecars.
type Preparer struct {
	images   ImagePreparer
	storage  *bwrap.RunStorage
	executor BackgroundExecutor
	network  PrivateNetworkStarter
	logger   *diagnostic.Logger
}

// New constructs the concrete sidecar preparer.
func New(
	images ImagePreparer,
	storage *bwrap.RunStorage,
	executor BackgroundExecutor,
	network PrivateNetworkStarter,
	logger *diagnostic.Logger,
) (*Preparer, error) {
	if isNilContract(images) {
		return nil, fmt.Errorf("sidecar image preparer is required")
	}
	if storage == nil {
		return nil, fmt.Errorf("sidecar run storage is required")
	}
	if isNilContract(executor) {
		return nil, fmt.Errorf("sidecar Bubblewrap executor is required")
	}
	if isNilContract(network) {
		return nil, fmt.Errorf("sidecar private network service is required")
	}

	return &Preparer{
		images:   images,
		storage:  storage,
		executor: executor,
		network:  network,
		logger:   logger,
	}, nil
}

// Resolve prepares and immediately releases an image to return immutable
// metadata for canonical resource planning.
func (s *Preparer) Resolve(
	ctx context.Context,
	definition Definition,
	progress mcpgateway.ProgressReporter,
) (result Metadata, returnErr error) {
	if s == nil || s.images == nil || s.storage == nil ||
		s.executor == nil || s.network == nil {
		return Metadata{}, fmt.Errorf("sidecar preparer is not configured")
	}
	if ctx == nil {
		return Metadata{}, fmt.Errorf("resolve sidecar context is nil")
	}
	if err := ctx.Err(); err != nil {
		return Metadata{}, err
	}
	if err := validateDefinition(definition); err != nil {
		return Metadata{}, err
	}

	preparedImage, err := s.images.PrepareImage(
		ctx,
		definition.Image,
		progress,
	)
	if err != nil {
		return Metadata{}, err
	}
	defer func() {
		s.logger.DebugError(
			"close resolved sidecar image",
			preparedImage.Close(),
		)
	}()

	if _, err := sidecarEnvironment(
		preparedImage.Spec().Runtime.Environment,
		definition.Environment,
	); err != nil {
		return Metadata{}, err
	}

	return imageMetadata(preparedImage)
}

// Prepare pins the image and every configured mount without starting a
// process.
func (s *Preparer) Prepare(
	ctx context.Context,
	definition Definition,
	progress mcpgateway.ProgressReporter,
) (result *Prepared, returnErr error) {
	if ctx == nil {
		return nil, fmt.Errorf("prepare sidecar context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	mounts, err := s.PinMounts(ctx, definition.Mounts)
	if err != nil {
		return nil, err
	}
	defer func() {
		s.logger.DebugError(
			"close sidecar mount capabilities after preparation",
			mounts.Close(),
		)
	}()

	return s.PreparePinned(ctx, definition, mounts, progress)
}

// PinMounts opens every configured source once and retains those exact inodes
// for later reusable-process generation starts.
func (s *Preparer) PinMounts(
	ctx context.Context,
	definition []mcpgateway.Mount,
) (result *MountCapabilities, returnErr error) {
	if s == nil {
		return nil, fmt.Errorf("sidecar preparer is not configured")
	}
	if ctx == nil {
		return nil, fmt.Errorf("pin sidecar mounts context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateMounts(definition); err != nil {
		return nil, err
	}

	binds := make([]mount.Bind, len(definition))
	sources := make(map[string]*os.File, len(definition))
	defer func() {
		if returnErr != nil {
			s.logger.DebugError(
				"close sidecar mount sources after pin failure",
				closeFiles(sources),
			)
		}
	}()
	for index, item := range definition {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		source, err := openMount(item.Source, s.logger)
		if err != nil {
			return nil, fmt.Errorf(
				"open sidecar mount %d: %w",
				index,
				err,
			)
		}
		if err := validateMountSource(source, item.Access); err != nil {
			s.logger.DebugError(
				"close invalid sidecar mount source",
				source.Close(),
			)
			return nil, fmt.Errorf(
				"validate sidecar mount %d: %w",
				index,
				err,
			)
		}
		binds[index] = mount.Bind{
			HostPath: item.Source,
			Target:   item.Target,
			Access:   item.Access,
		}
		sources[item.Target] = source
	}

	return &MountCapabilities{
		definition: append(
			[]mcpgateway.Mount(nil),
			definition...,
		),
		binds:   binds,
		sources: sources,
		logger:  s.logger,
	}, nil
}

// PreparePinned prepares an image and duplicates an already-pinned mount set
// without reopening a configured host pathname.
func (s *Preparer) PreparePinned(
	ctx context.Context,
	definition Definition,
	mounts *MountCapabilities,
	progress mcpgateway.ProgressReporter,
) (result *Prepared, returnErr error) {
	if s == nil || s.images == nil || s.storage == nil ||
		s.executor == nil {
		return nil, fmt.Errorf("sidecar preparer is not configured")
	}
	if ctx == nil {
		return nil, fmt.Errorf("prepare sidecar context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateDefinition(definition); err != nil {
		return nil, err
	}
	definition = cloneDefinition(definition)

	binds, sources, err := mounts.duplicate(definition.Mounts)
	if err != nil {
		return nil, err
	}
	defer func() {
		if returnErr != nil {
			s.logger.DebugError(
				"close duplicated sidecar mount sources after preparation failure",
				closeFiles(sources),
			)
		}
	}()

	preparedImage, err := s.images.PrepareImage(
		ctx,
		definition.Image,
		progress,
	)
	if err != nil {
		return nil, fmt.Errorf("prepare sidecar OCI image: %w", err)
	}
	defer func() {
		if returnErr != nil {
			s.logger.DebugError(
				"close sidecar image after preparation failure",
				preparedImage.Close(),
			)
		}
	}()

	environment, err := sidecarEnvironment(
		preparedImage.Spec().Runtime.Environment,
		definition.Environment,
	)
	if err != nil {
		return nil, err
	}
	metadata, err := imageMetadata(preparedImage)
	if err != nil {
		return nil, err
	}

	return &Prepared{
		preparer:    s,
		image:       preparedImage,
		definition:  definition,
		metadata:    metadata,
		binds:       binds,
		bindSources: sources,
		environment: environment,
	}, nil
}
