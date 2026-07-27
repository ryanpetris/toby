package storage

// Lists, inspects, resolves, and removes persistent Toby volumes.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/crypto/blake2b"

	"petris.dev/toby/internal/storage/safefs"
)

const (
	volumeCatalogEntries = 100_000
	volumeIDShortLength  = 12
)

// VolumeInfo describes one persistent Toby volume and its native paths.
type VolumeInfo struct {
	ID           string
	Type         VolumeType
	Name         string
	Profile      string
	Purpose      string
	ObjectPath   string
	DataPath     string
	MetadataPath string
	Problem      string
}

// ShortID returns the display prefix accepted by the volume selector.
func (v VolumeInfo) ShortID() string {
	if len(v.ID) <= volumeIDShortLength {
		return v.ID
	}
	return v.ID[:volumeIDShortLength]
}

// ListVolumes returns published volume objects matching every nonempty filter
// field. A malformed individual object remains present for an empty filter
// with Problem set so it can still be identified and removed.
func (s *Store) ListVolumes(
	ctx context.Context,
	filter VolumeFilter,
) ([]VolumeInfo, error) {
	if err := s.validateContext(ctx); err != nil {
		return nil, err
	}
	filter, err := normalizeVolumeFilter(filter)
	if err != nil {
		return nil, err
	}

	ids, err := s.volumeIDs()
	if err != nil {
		return nil, err
	}

	volumes := make([]VolumeInfo, 0, len(ids))
	for _, id := range ids {
		info, inspectErr := s.inspectVolume(id)
		if inspectErr != nil {
			info.Problem = inspectErr.Error()
		}
		if !filter.matches(info) {
			continue
		}
		volumes = append(volumes, info)
	}
	sort.Slice(volumes, func(i, j int) bool {
		left := volumes[i]
		right := volumes[j]
		for _, comparison := range [][2]string{
			{string(left.Type), string(right.Type)},
			{left.Name, right.Name},
			{left.Profile, right.Profile},
			{left.Purpose, right.Purpose},
			{left.ID, right.ID},
		} {
			if comparison[0] != comparison[1] {
				return comparison[0] < comparison[1]
			}
		}
		return false
	})
	return volumes, nil
}

// InspectVolumeBySpec derives one volume's canonical ID from a complete
// specification and returns its verified metadata and paths.
func (s *Store) InspectVolumeBySpec(
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
	id, _, err := volumeID(metadata, DefaultLimits().MetadataSize)
	if err != nil {
		return VolumeInfo{}, fmt.Errorf("derive volume identity: %w", err)
	}

	info, err := s.inspectVolume(id)
	if errors.Is(err, fs.ErrNotExist) {
		err = fmt.Errorf("volume %q does not exist", id)
	}
	if err != nil {
		info.Problem = err.Error()
	}
	return info, err
}

// InspectVolume resolves an ID or displayed ID prefix and returns its verified
// metadata and paths.
func (s *Store) InspectVolume(
	ctx context.Context,
	selector string,
) (VolumeInfo, error) {
	if err := s.validateContext(ctx); err != nil {
		return VolumeInfo{}, err
	}

	id, err := s.resolveVolumeSelector(selector)
	if err != nil {
		return VolumeInfo{}, err
	}

	info, err := s.inspectVolume(id)
	if err != nil {
		info.Problem = err.Error()
	}
	return info, err
}

// RemoveVolumes resolves IDs or displayed ID prefixes, acquires every
// exclusive lifecycle lease, and then removes the selected volumes. A volume
// retained by any running launch prevents the batch from beginning. Progress
// begins only after all selected volumes pass that lease preflight.
func (s *Store) RemoveVolumes(
	ctx context.Context,
	selectors []string,
	reporter VolumeRemovalReporter,
) (removedIDs []string, returnErr error) {
	if err := s.validateContext(ctx); err != nil {
		return nil, err
	}
	if len(selectors) == 0 {
		return nil, fmt.Errorf("volume removal selection is empty")
	}

	catalogIDs, err := s.volumeIDs()
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(selectors))
	seen := make(map[string]bool, len(selectors))
	for _, selector := range selectors {
		id, err := resolveVolumeSelectorFrom(
			selector,
			catalogIDs,
		)
		if err != nil {
			return nil, err
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}

	retained := make([]*volumeRemoval, 0, len(ids))
	defer func() {
		s.logger.DebugError(
			"release retained volume removals",
			closeVolumeRemovals(retained),
		)
	}()
	for _, id := range ids {
		removal, err := s.retainVolumeRemoval(id)
		if err != nil {
			return nil, err
		}
		retained = append(retained, removal)
	}

	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return removedIDs, err
		}
		reporter.report(VolumeRemovalProgress{
			ID:    id,
			Phase: VolumeRemovalPhaseRemoving,
		})
		if err := s.volumes.RemoveAll(id, ^uint64(0)); err != nil {
			reporter.report(VolumeRemovalProgress{
				ID:    id,
				Phase: VolumeRemovalPhaseFailed,
			})
			return removedIDs, fmt.Errorf(
				"remove volume %q: %w",
				id,
				err,
			)
		}
		removedIDs = append(removedIDs, id)
		reporter.report(VolumeRemovalProgress{
			ID:    id,
			Phase: VolumeRemovalPhaseRemoved,
		})
	}
	return removedIDs, nil
}

type volumeRemoval struct {
	object *safefs.Directory
	data   *safefs.Directory
	lease  *safefs.Lock
}

func (s *Store) retainVolumeRemoval(
	id string,
) (_ *volumeRemoval, returnErr error) {
	removal := &volumeRemoval{}
	defer func() {
		if returnErr != nil {
			s.logger.DebugError(
				"close incomplete volume removal",
				removal.close(),
				"volume_id", id,
			)
		}
	}()

	object, err := s.volumes.OpenDirectory(id)
	if err != nil {
		return nil, fmt.Errorf("open volume %q: %w", id, err)
	}
	removal.object = object

	data, err := object.OpenDirectory(volumeDataDirectory)
	if errors.Is(err, fs.ErrNotExist) {
		return removal, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open volume data %q: %w", id, err)
	}
	removal.data = data

	lease, err := data.LockSelf(safefs.LockExclusive, true)
	if errors.Is(err, safefs.ErrWouldBlock) {
		return nil, fmt.Errorf("%w: volume %q", ErrVolumeBusy, id)
	}
	if err != nil {
		return nil, fmt.Errorf("lock volume %q for removal: %w", id, err)
	}
	removal.lease = lease
	return removal, nil
}

func (r *volumeRemoval) close() error {
	if r == nil {
		return nil
	}

	var err error
	if r.lease != nil {
		err = r.lease.Close()
	}
	r.lease = nil
	if r.data != nil {
		err = errors.Join(err, r.data.Close())
	}
	r.data = nil
	if r.object != nil {
		err = errors.Join(err, r.object.Close())
	}
	r.object = nil
	return err
}

func closeVolumeRemovals(removals []*volumeRemoval) error {
	var result error
	for index := len(removals) - 1; index >= 0; index-- {
		result = errors.Join(result, removals[index].close())
	}
	return result
}

func (s *Store) inspectVolume(id string) (info VolumeInfo, returnErr error) {
	info.ID = id

	object, err := s.volumes.OpenDirectory(id)
	if err != nil {
		return info, fmt.Errorf("open volume object: %w", err)
	}
	defer func() {
		s.logger.DebugError(
			"close inspected volume object",
			object.Close(),
			"volume_id", id,
		)
	}()
	object.RepairPrivateOwnershipAndMode()

	info.ObjectPath, err = resolvedDirectoryPath(object)
	if err != nil {
		return info, fmt.Errorf("resolve volume object path: %w", err)
	}
	info.MetadataPath = filepath.Join(info.ObjectPath, volumeMetadataFile)

	data, err := object.ReadFile(
		volumeMetadataFile,
		DefaultLimits().MetadataSize,
	)
	if err != nil {
		return info, fmt.Errorf("read volume metadata: %w", err)
	}
	var metadata volumeMetadata
	if err := decodeMetadata(
		data,
		DefaultLimits().MetadataSize,
		&metadata,
	); err != nil {
		return info, fmt.Errorf("%w: decode volume metadata: %v", ErrMetadataMismatch, err)
	}
	if err := metadata.validate(); err != nil {
		return info, fmt.Errorf("%w: validate volume metadata: %v", ErrMetadataMismatch, err)
	}
	actualID, _, err := volumeID(metadata, DefaultLimits().MetadataSize)
	if err != nil {
		return info, fmt.Errorf("%w: derive stored volume identity: %v", ErrMetadataMismatch, err)
	}
	if actualID != id {
		return info, fmt.Errorf(
			"%w: metadata identifies volume %q",
			ErrMetadataMismatch,
			actualID,
		)
	}

	info.Type = metadata.Type
	info.Name = metadata.Name
	info.Profile = metadata.Profile
	info.Purpose = metadata.Purpose

	directory, err := object.OpenDirectory(volumeDataDirectory)
	if err != nil {
		return info, fmt.Errorf("open volume data: %w", err)
	}
	defer func() {
		s.logger.DebugError(
			"close inspected volume data",
			directory.Close(),
			"volume_id", id,
		)
	}()

	lease, err := directory.LockSelf(safefs.LockShared, true)
	if errors.Is(err, safefs.ErrWouldBlock) {
		return info, fmt.Errorf("%w: volume %q is being removed", ErrVolumeBusy, id)
	}
	if err != nil {
		return info, fmt.Errorf("retain volume %q for inspection: %w", id, err)
	}
	defer func() {
		s.logger.DebugError(
			"release inspected volume lease",
			lease.Close(),
			"volume_id", id,
		)
	}()

	info.DataPath, err = resolvedDirectoryPath(directory)
	if err != nil {
		return info, fmt.Errorf("resolve volume data path: %w", err)
	}
	return info, nil
}

func (s *Store) resolveVolumeSelector(selector string) (string, error) {
	ids, err := s.volumeIDs()
	if err != nil {
		return "", err
	}
	return resolveVolumeSelectorFrom(selector, ids)
}

func resolveVolumeSelectorFrom(
	selector string,
	ids []string,
) (string, error) {
	selector = strings.TrimSpace(selector)
	if len(selector) < volumeIDShortLength ||
		len(selector) > blake2b.Size*2 ||
		!isLowerHex(selector) {
		return "", fmt.Errorf(
			"volume selector must be %d to %d lowercase hexadecimal characters",
			volumeIDShortLength,
			blake2b.Size*2,
		)
	}

	var match string
	for _, id := range ids {
		if !strings.HasPrefix(id, selector) {
			continue
		}
		if match != "" {
			return "", fmt.Errorf("volume selector %q is ambiguous", selector)
		}
		match = id
	}
	if match == "" {
		return "", fmt.Errorf("volume %q does not exist", selector)
	}
	return match, nil
}

func (s *Store) volumeIDs() ([]string, error) {
	names, err := s.volumes.Names(volumeCatalogEntries)
	if err != nil {
		return nil, fmt.Errorf("list volume objects: %w", err)
	}

	ids := make([]string, 0, len(names))
	for _, name := range names {
		if isVolumeID(name) {
			ids = append(ids, name)
		}
	}
	return ids, nil
}

func isVolumeID(value string) bool {
	return len(value) == blake2b.Size*2 && isLowerHex(value)
}

func isLowerHex(value string) bool {
	for _, character := range value {
		if (character >= '0' && character <= '9') ||
			(character >= 'a' && character <= 'f') {
			continue
		}
		return false
	}
	return true
}
