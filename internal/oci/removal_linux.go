//go:build linux

package oci

// Removes reference mappings and unreferenced immutable objects under
// cross-process reference and object locks.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"

	"petris.dev/toby/internal/storage/safefs"
)

// RemoveImages removes a snapshot of selected reference or object catalog
// entries. Force permits references to be removed while their final object is
// busy, leaving that object dangling.
func (s *Store) RemoveImages(
	ctx context.Context,
	selections []ImageInfo,
	force bool,
	reporter ImageRemovalReporter,
) (removed []ImageInfo, returnErr error) {
	if err := validateImageContext(ctx); err != nil {
		return nil, err
	}
	if len(selections) == 0 {
		return nil, fmt.Errorf("image removal selection is empty")
	}
	if err := s.startOperation(); err != nil {
		return nil, err
	}
	defer s.finishOperation()

	catalog, err := s.readImageCatalog(ctx)
	if err != nil {
		return nil, err
	}
	selected, err := resolveRemovalSelections(selections, catalog)
	if err != nil {
		return nil, err
	}

	referenceIDs := selectedReferenceIDs(selected, catalog, force)
	referenceLocks, err := s.retainReferenceRemovals(referenceIDs)
	if err != nil {
		return nil, err
	}
	defer func() {
		s.logger.DebugError(
			"release OCI reference-removal locks",
			closeImageLocks(referenceLocks),
		)
	}()

	catalog, err = s.readImageCatalog(ctx)
	if err != nil {
		return nil, err
	}
	selected, err = resolveRemovalSelections(selected, catalog)
	if err != nil {
		return nil, err
	}

	candidates, allowBusy, err := removalObjectCandidates(
		selected,
		catalog,
		force,
		referenceIDs,
	)
	if err != nil {
		return nil, err
	}
	objectLocks, busyObjects, err := s.retainObjectRemovals(
		candidates,
		allowBusy,
	)
	if err != nil {
		return nil, err
	}
	defer func() {
		s.logger.DebugError(
			"release OCI object-removal locks",
			closeImageLocks(objectLocks),
		)
	}()

	remainingReferences, err := s.referenceObjectCounts(referenceIDs)
	if err != nil {
		return nil, err
	}

	referencesDirectory, err := s.root.OpenDirectory("references")
	if err != nil {
		return nil, fmt.Errorf("open OCI references for removal: %w", err)
	}
	defer func() {
		s.logger.DebugError(
			"close OCI references directory",
			referencesDirectory.Close(),
		)
	}()

	for _, info := range selected {
		reporter.report(ImageRemovalProgress{
			ID:    info.ID,
			Phase: ImageRemovalPhaseRemoving,
		})
	}

	deletedReferences := make(map[string]bool, len(referenceIDs))
	for _, id := range sortedSet(referenceIDs) {
		if err := referencesDirectory.RemoveAll(id+".json", 1); err != nil {
			removed = reportImageRemovalResults(
				selected,
				deletedReferences,
				nil,
				true,
				reporter,
			)
			return removed, fmt.Errorf(
				"remove OCI reference %q: %w", id, err,
			)
		}
		deletedReferences[id] = true
	}

	deletedObjects := make(map[string]bool)
	for _, object := range sortedSet(candidates) {
		if busyObjects[object] || remainingReferences[object] != 0 {
			continue
		}
		if _, retained := objectLocks[object]; !retained {
			continue
		}
		if err := s.root.RemoveAll(
			filepath.Join("objects", object),
			^uint64(0),
		); err != nil {
			removed = reportImageRemovalResults(
				selected,
				deletedReferences,
				deletedObjects,
				true,
				reporter,
			)
			return removed, fmt.Errorf(
				"remove OCI object %q: %w",
				object,
				err,
			)
		}
		deletedObjects[object] = true
	}

	for _, info := range selected {
		if info.Kind == ImageEntryObject &&
			!deletedObjects[info.ObjectKey] {
			if len(info.References) == 0 {
				removed = reportImageRemovalResults(
					selected,
					deletedReferences,
					deletedObjects,
					true,
					reporter,
				)
				return removed, fmt.Errorf(
					"%w: OCI object %s",
					ErrImageBusy,
					info.Manifest.Digest,
				)
			}
		}
	}
	return reportImageRemovalResults(
		selected,
		deletedReferences,
		deletedObjects,
		false,
		reporter,
	), nil
}

func reportImageRemovalResults(
	selected []ImageInfo,
	deletedReferences map[string]bool,
	deletedObjects map[string]bool,
	failed bool,
	reporter ImageRemovalReporter,
) []ImageInfo {
	removed := make([]ImageInfo, 0, len(selected))
	for _, info := range selected {
		phase := ImageRemovalPhaseFailed
		switch info.Kind {
		case ImageEntryReference:
			if deletedReferences[info.ID] {
				phase = ImageRemovalPhaseUntagged
				if deletedObjects[info.ObjectKey] {
					phase = ImageRemovalPhaseRemoved
				}
				removed = append(removed, cloneImageInfo(info))
			}
		case ImageEntryObject:
			if deletedObjects[info.ObjectKey] {
				phase = ImageRemovalPhaseRemoved
				removed = append(removed, cloneImageInfo(info))
			} else if !failed && len(info.References) != 0 {
				phase = ImageRemovalPhaseUntagged
				removed = append(removed, cloneImageInfo(info))
			}
		}
		reporter.report(ImageRemovalProgress{
			ID:    info.ID,
			Phase: phase,
		})
	}
	return removed
}

func resolveRemovalSelections(
	input []ImageInfo,
	catalog imageCatalog,
) ([]ImageInfo, error) {
	result := make([]ImageInfo, 0, len(input))
	seen := make(map[string]bool)
	for _, requested := range input {
		var current ImageInfo
		found := false
		switch requested.Kind {
		case ImageEntryReference:
			for _, entry := range catalog.entries {
				if entry.Kind == ImageEntryReference &&
					entry.ID == requested.ID {
					current = entry
					found = true
					break
				}
			}
		case ImageEntryObject:
			for _, object := range catalog.objects {
				if object.ID == requested.ID ||
					(requested.ObjectKey != "" &&
						object.ObjectKey == requested.ObjectKey) {
					current = object
					found = true
					break
				}
			}
		default:
			return nil, fmt.Errorf(
				"unknown image selection kind %q",
				requested.Kind,
			)
		}
		if !found {
			return nil, fmt.Errorf(
				"OCI image %q no longer exists",
				requested.ID,
			)
		}
		key := string(current.Kind) + "\x00" + current.ID
		if !seen[key] {
			seen[key] = true
			result = append(result, current)
		}
	}
	return result, nil
}

func selectedReferenceIDs(
	selected []ImageInfo,
	catalog imageCatalog,
	force bool,
) map[string]bool {
	result := make(map[string]bool)
	for _, info := range selected {
		if info.Kind == ImageEntryReference {
			result[info.ID] = true
			continue
		}
		if !force {
			continue
		}
		for _, entry := range catalog.entries {
			if entry.Kind == ImageEntryReference &&
				entry.ObjectKey == info.ObjectKey {
				result[entry.ID] = true
			}
		}
	}
	return result
}

func removalObjectCandidates(
	selected []ImageInfo,
	catalog imageCatalog,
	force bool,
	selectedReferences map[string]bool,
) (
	candidates map[string]bool,
	allowBusy map[string]bool,
	err error,
) {
	candidates = make(map[string]bool)
	allowBusy = make(map[string]bool)

	for _, info := range selected {
		if info.ObjectKey == "" {
			continue
		}
		if info.Kind == ImageEntryObject {
			if len(info.References) != 0 && !force {
				return nil, nil, fmt.Errorf(
					"OCI object %s still has references; use --force to remove them",
					info.Manifest.Digest,
				)
			}
			candidates[info.ObjectKey] = true
			if force && len(info.References) != 0 {
				allowBusy[info.ObjectKey] = true
			}
			continue
		}

		allSelected := true
		for _, entry := range catalog.entries {
			if entry.Kind == ImageEntryReference &&
				entry.ObjectKey == info.ObjectKey &&
				!selectedReferences[entry.ID] {
				allSelected = false
				break
			}
		}
		if allSelected {
			candidates[info.ObjectKey] = true
			if force {
				allowBusy[info.ObjectKey] = true
			}
		}
	}
	return candidates, allowBusy, nil
}

func (s *Store) retainReferenceRemovals(
	ids map[string]bool,
) (map[string]*safefs.Lock, error) {
	result := make(map[string]*safefs.Lock, len(ids))
	for _, id := range sortedSet(ids) {
		lock, err := s.root.Lock(
			filepath.Join("locks", "references", id+".lock"),
			safefs.LockExclusive,
			true,
		)
		if errors.Is(err, safefs.ErrWouldBlock) {
			s.logger.DebugError(
				"release partially acquired OCI reference-removal locks",
				closeImageLocks(result),
			)
			return nil, fmt.Errorf(
				"%w: OCI reference %q",
				ErrImageBusy,
				id,
			)
		}
		if err != nil {
			s.logger.DebugError(
				"release partially acquired OCI reference-removal locks",
				closeImageLocks(result),
			)
			return nil, fmt.Errorf("lock OCI reference %q: %w", id, err)
		}
		result[id] = lock
	}
	return result, nil
}

func (s *Store) retainObjectRemovals(
	objects map[string]bool,
	allowBusy map[string]bool,
) (
	retained map[string]*safefs.Lock,
	busy map[string]bool,
	returnErr error,
) {
	retained = make(map[string]*safefs.Lock, len(objects))
	busy = make(map[string]bool)
	for _, object := range sortedSet(objects) {
		lock, err := s.root.Lock(
			objectLockName(object),
			safefs.LockExclusive,
			true,
		)
		if errors.Is(err, safefs.ErrWouldBlock) {
			if allowBusy[object] {
				busy[object] = true
				continue
			}
			s.logger.DebugError(
				"release partially acquired OCI object-removal locks",
				closeImageLocks(retained),
			)
			return nil, nil, fmt.Errorf(
				"%w: OCI object %q",
				ErrImageBusy,
				object,
			)
		}
		if err != nil {
			s.logger.DebugError(
				"release partially acquired OCI object-removal locks",
				closeImageLocks(retained),
			)
			return nil, nil, fmt.Errorf(
				"lock OCI object %q: %w",
				object,
				err,
			)
		}
		retained[object] = lock
	}
	return retained, busy, nil
}

func (s *Store) referenceObjectCounts(
	excluded map[string]bool,
) (result map[string]int, returnErr error) {
	references, err := s.root.OpenDirectory("references")
	if err != nil {
		return nil, err
	}
	defer func() {
		s.logger.DebugError(
			"close OCI references directory",
			references.Close(),
		)
	}()

	names, err := references.Names(imageCatalogEntries)
	if err != nil {
		return nil, err
	}
	result = make(map[string]int)
	for _, name := range names {
		id := filepath.Base(name)
		id = id[:len(id)-len(filepath.Ext(id))]
		if excluded[id] {
			continue
		}
		data, err := references.ReadFile(name, maximumMetadataBytes)
		if err != nil {
			continue
		}
		var record referenceRecord
		if decodeMetadata(data, &record) == nil &&
			record.Object != "" &&
			filepath.IsLocal(record.Object) {
			result[record.Object]++
		}
	}
	return result, nil
}

func closeImageLocks(locks map[string]*safefs.Lock) error {
	keys := make([]string, 0, len(locks))
	for key := range locks {
		keys = append(keys, key)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))

	var result error
	for _, key := range keys {
		result = errors.Join(result, locks[key].Close())
	}
	return result
}

func sortedSet(values map[string]bool) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
