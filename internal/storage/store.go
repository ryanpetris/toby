package storage

// Coordinates per-user volume creation, validation, first-use seeding, and
// capability retention without application or home singleton locks.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/sandbox/mount"
	"petris.dev/toby/internal/storage/recovery"
	"petris.dev/toby/internal/storage/safefs"
)

const (
	publicationCleanupEntries     = 2_000_000
	publicationRecoveryCandidates = 4096
	volumeDataDirectory           = "_data"
	volumeMetadataFile            = "metadata.json"
)

// Store owns the per-user persistent volume root for one host process.
type Store struct {
	dataRoot *safefs.Directory
	volumes  *safefs.Directory
	uid      int
	gid      int
	limits   Limits
	logger   *diagnostic.Logger
}

// NewStore securely opens or creates the per-user Toby volume root.
func NewStore(
	paths config.Paths,
	limits Limits,
	diagnostics *diagnostic.Service,
) (*Store, error) {
	if err := limits.validate(); err != nil {
		return nil, err
	}

	uid, gid := os.Geteuid(), os.Getegid()
	logger := diagnostics.Logger("storage")
	dataRoot, err := safefs.OpenOrCreateRoot(
		paths.TobyDataDir(),
		safefs.DirectoryOptions{
			OwnerUID: uid,
			OwnerGID: gid,
			Logger:   logger,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("open Toby data root: %w", err)
	}
	volumes, err := dataRoot.MkdirAll("volumes")
	if err != nil {
		logger.DebugError(
			"close Toby data root after volume root setup failed",
			dataRoot.Close(),
		)
		return nil, fmt.Errorf("open volume root: %w", err)
	}
	volumes.RepairPrivateOwnershipAndMode()
	if err := recovery.CleanupTemporaryDirectories(
		volumes,
		publicationRecoveryCandidates,
		publicationCleanupEntries,
	); err != nil {
		logger.DebugError(
			"recover interrupted volume publications",
			err,
		)
	}

	return &Store{
		dataRoot: dataRoot,
		volumes:  volumes,
		uid:      uid,
		gid:      gid,
		limits:   limits,
		logger:   logger,
	}, nil
}

// Close releases retained storage-root capabilities.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.logger.DebugError(
		"close volume root",
		closeDirectory(&s.volumes),
	)
	s.logger.DebugError(
		"close Toby data root",
		closeDirectory(&s.dataRoot),
	)
	return nil
}

// CreateVolume atomically publishes an empty volume from a complete
// specification. An existing valid volume with the same identity is returned
// unchanged.
func (s *Store) CreateVolume(
	ctx context.Context,
	spec VolumeSpec,
) (VolumeInfo, error) {
	if err := s.validateContext(ctx); err != nil {
		return VolumeInfo{}, err
	}

	_, metadata, err := normalizeVolumeSpec(spec)
	if err != nil {
		return VolumeInfo{}, err
	}
	id, metadataData, err := volumeID(
		metadata,
		DefaultLimits().MetadataSize,
	)
	if err != nil {
		return VolumeInfo{}, fmt.Errorf("derive volume identity: %w", err)
	}

	volume, err := s.openVolume(id, metadata)
	if err == nil {
		s.logger.DebugError(
			"close existing volume after creation lookup",
			volume.Close(),
			"volume_id", id,
		)
		return s.inspectVolume(id)
	}
	if !errors.Is(err, errStorageObjectMissing) {
		return VolumeInfo{}, err
	}

	if err := s.publishVolume(
		ctx,
		id,
		metadataData,
		SeedSource{},
	); err != nil {
		return VolumeInfo{},
			fmt.Errorf("publish volume %q: %w", id, err)
	}
	return s.inspectVolume(id)
}

// ResolveHome opens a verified existing home volume or races an atomic
// first-use seed and publication. A losing publisher reuses the winner.
func (s *Store) ResolveHome(
	ctx context.Context,
	name string,
	profile string,
	seed SeedSource,
) (*HomeHandle, error) {
	if err := s.validateContext(ctx); err != nil {
		return nil, err
	}

	identity, err := ResolveHomeIdentity(name, profile)
	if err != nil {
		return nil, err
	}
	metadata := newHomeMetadata(identity.Profile, identity.DisplayName)
	id, metadataData, err := volumeID(metadata, DefaultLimits().MetadataSize)
	if err != nil {
		return nil, fmt.Errorf("derive home volume identity: %w", err)
	}
	if id != identity.ID {
		return nil, fmt.Errorf("%w: inconsistent home volume identity", ErrMetadataMismatch)
	}

	volume, err := s.openVolume(id, metadata)
	if err == nil {
		return newHomeHandle(identity, volume), nil
	}
	if !errors.Is(err, errStorageObjectMissing) {
		return nil, err
	}

	if err := s.publishVolume(ctx, id, metadataData, seed); err != nil {
		return nil, fmt.Errorf("publish home volume %q: %w", id, err)
	}
	volume, err = s.openVolume(id, metadata)
	if err != nil {
		return nil, err
	}
	return newHomeHandle(identity, volume), nil
}

// ResolveManaged normalizes the complete request set before mutation and
// resolves every tool request through its selected global profile.
func (s *Store) ResolveManaged(
	ctx context.Context,
	profiles ProfileSelection,
	requests []mount.Request,
	occupiedTargets []string,
	rootfs SeedSource,
) ([]*ManagedHandle, error) {
	if err := s.validateContext(ctx); err != nil {
		return nil, err
	}
	normalized, err := normalizeRequests(requests, occupiedTargets)
	if err != nil {
		return nil, err
	}

	handles := make([]*ManagedHandle, 0, len(normalized))
	for _, request := range normalized {
		handle, err := s.resolveManaged(
			ctx,
			profiles.ProfileFor(request.Key.Name),
			request,
			rootfs,
		)
		if err != nil {
			s.logger.DebugError(
				"close resolved tool volumes after resolution failed",
				closeManagedHandles(handles),
			)
			return nil, err
		}
		handles = append(handles, handle)
	}
	return handles, nil
}

func (s *Store) resolveManaged(
	ctx context.Context,
	profile string,
	request mount.Request,
	rootfs SeedSource,
) (*ManagedHandle, error) {
	metadata := newToolMetadata(profile, request.Key.Name, request.Key.Purpose)
	id, metadataData, err := volumeID(metadata, DefaultLimits().MetadataSize)
	if err != nil {
		return nil, fmt.Errorf("derive tool volume identity %s: %w", request.Key, err)
	}

	volume, err := s.openVolume(id, metadata)
	if err == nil {
		return newManagedHandle(profile, request, volume), nil
	}
	if !errors.Is(err, errStorageObjectMissing) {
		return nil, err
	}

	seed := rootfs
	seed.ImagePath = request.Seed.ImagePath
	if err := s.publishVolume(ctx, id, metadataData, seed); err != nil {
		return nil, fmt.Errorf("publish tool volume %s: %w", request.Key, err)
	}
	volume, err = s.openVolume(id, metadata)
	if err != nil {
		return nil, err
	}
	return newManagedHandle(profile, request, volume), nil
}

func (s *Store) publishVolume(
	ctx context.Context,
	id string,
	metadata []byte,
	seed SeedSource,
) error {
	_, err := s.volumes.PublishDirectory(
		id,
		publicationCleanupEntries,
		func(stage *safefs.Directory) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			data, err := stage.MkdirAll(volumeDataDirectory)
			if err != nil {
				return err
			}
			defer func() {
				s.logger.DebugError(
					"close volume data directory after publication",
					data.Close(),
					"volume_id", id,
				)
			}()

			if err := s.seedDirectory(ctx, data, seed); err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			created, err := stage.PublishFile(volumeMetadataFile, metadata, 0o600)
			if err != nil {
				return err
			}
			if !created {
				return fmt.Errorf("volume stage unexpectedly contains metadata")
			}
			return ctx.Err()
		},
	)
	return err
}

func (s *Store) openVolume(
	id string,
	expected volumeMetadata,
) (*openedVolume, error) {
	object, err := s.volumes.OpenDirectory(id)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: volume %q", errStorageObjectMissing, id)
		}
		return nil, err
	}
	defer func() {
		s.logger.DebugError(
			"close volume object",
			object.Close(),
			"volume_id", id,
		)
	}()
	object.RepairPrivateOwnershipAndMode()

	data, err := object.ReadFile(
		volumeMetadataFile,
		DefaultLimits().MetadataSize,
	)
	if err != nil {
		return nil, fmt.Errorf("read volume metadata: %w", err)
	}
	var metadata volumeMetadata
	if err := decodeMetadata(
		data,
		DefaultLimits().MetadataSize,
		&metadata,
	); err != nil {
		return nil, fmt.Errorf("%w: decode volume metadata: %v", ErrMetadataMismatch, err)
	}
	if err := metadata.validate(); err != nil {
		return nil, fmt.Errorf("%w: validate volume metadata: %v", ErrMetadataMismatch, err)
	}
	actualID, _, err := volumeID(metadata, DefaultLimits().MetadataSize)
	if err != nil {
		return nil, fmt.Errorf("%w: derive stored volume identity: %v", ErrMetadataMismatch, err)
	}
	if metadata != expected || actualID != id {
		return nil, fmt.Errorf("%w: volume %q", ErrMetadataMismatch, id)
	}

	directory, err := object.OpenDirectory(volumeDataDirectory)
	if err != nil {
		return nil, fmt.Errorf("open volume data: %w", err)
	}

	lease, err := directory.LockSelf(safefs.LockShared, true)
	if err != nil {
		if errors.Is(err, safefs.ErrWouldBlock) {
			s.logger.DebugError(
				"close busy volume data directory",
				directory.Close(),
				"volume_id", id,
			)
			return nil, fmt.Errorf("%w: volume %q", ErrVolumeBusy, id)
		}
		s.logger.DebugError(
			"close volume data directory after lease failed",
			directory.Close(),
			"volume_id", id,
		)
		return nil, fmt.Errorf("retain volume %q: %w", id, err)
	}

	return &openedVolume{
		data:  directory,
		lease: lease,
	}, nil
}

func (s *Store) validateContext(ctx context.Context) error {
	if s == nil || s.dataRoot == nil || s.volumes == nil {
		return fmt.Errorf("volume store is not initialized")
	}
	if ctx == nil {
		return fmt.Errorf("storage context is nil")
	}
	return ctx.Err()
}

func closeDirectory(directory **safefs.Directory) error {
	if directory == nil || *directory == nil {
		return nil
	}
	err := (*directory).Close()
	*directory = nil
	return err
}

func closeManagedHandles(handles []*ManagedHandle) error {
	var result error
	for _, handle := range handles {
		if handle != nil {
			result = errors.Join(result, handle.Close())
		}
	}
	return result
}
