//go:build linux

package oci

// Enumerates, validates, filters, and resolves per-user OCI cache entries.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"petris.dev/toby/internal/oci/image"
)

const imageCatalogEntries = 100_000

type imageCatalog struct {
	entries []ImageInfo
	objects map[string]ImageInfo
}

// ListImages returns one row per reference plus one row for each dangling
// immutable object. Malformed entries remain visible with Problem populated.
func (s *Store) ListImages(
	ctx context.Context,
	filter ImageFilter,
) ([]ImageInfo, error) {
	if err := validateImageContext(ctx); err != nil {
		return nil, err
	}
	if err := s.startOperation(); err != nil {
		return nil, err
	}
	defer s.finishOperation()

	filter, err := filter.normalize()
	if err != nil {
		return nil, err
	}
	catalog, err := s.readImageCatalog(ctx)
	if err != nil {
		return nil, err
	}

	result := make([]ImageInfo, 0, len(catalog.entries))
	for _, info := range catalog.entries {
		if filter.matches(info) {
			result = append(result, cloneImageInfo(info))
		}
	}
	return result, nil
}

// InspectImage resolves a reference, reference-record ID, object ID, or
// manifest digest. Reference selection defaults to the current Linux platform.
func (s *Store) InspectImage(
	ctx context.Context,
	selector string,
	platform ocispec.Platform,
) (ImageInfo, error) {
	if err := validateImageContext(ctx); err != nil {
		return ImageInfo{}, err
	}
	if err := s.startOperation(); err != nil {
		return ImageInfo{}, err
	}
	defer s.finishOperation()

	selector = strings.TrimSpace(selector)
	if selector == "" {
		return ImageInfo{}, fmt.Errorf("image selector must not be blank")
	}
	platform, err := image.NormalizePlatform(
		defaultImagePlatform(platform),
	)
	if err != nil {
		return ImageInfo{}, err
	}
	catalog, err := s.readImageCatalog(ctx)
	if err != nil {
		return ImageInfo{}, err
	}

	if strings.HasPrefix(selector, "sha256:") {
		return resolveObjectDigest(selector, platform, catalog.objects)
	}
	if isHexPrefix(selector) {
		info, found, err := resolveImageIDPrefix(
			selector,
			platform,
			catalog,
		)
		if err != nil {
			return ImageInfo{}, err
		}
		if found {
			return info, nil
		}
	}

	request, err := normalizeRequest(Request{
		Reference: selector,
		Platform:  platform,
	})
	if err != nil {
		return ImageInfo{}, err
	}
	for _, info := range catalog.entries {
		if info.Kind == ImageEntryReference &&
			info.Reference == request.reference &&
			samePlatform(info.Platform, request.platform) {
			return cloneImageInfo(info), nil
		}
	}
	return ImageInfo{}, fmt.Errorf(
		"OCI image reference %q for %s does not exist",
		request.reference,
		formatImagePlatform(request.platform),
	)
}

func (s *Store) readImageCatalog(
	ctx context.Context,
) (imageCatalog, error) {
	if err := ctx.Err(); err != nil {
		return imageCatalog{}, err
	}

	objects, err := s.readCatalogObjects(ctx)
	if err != nil {
		return imageCatalog{}, err
	}
	references, referencedObjects, err := s.readCatalogReferences(objects)
	if err != nil {
		return imageCatalog{}, err
	}

	for key, info := range objects {
		sort.Strings(info.References)
		objects[key] = info
	}
	for index := range references {
		if object, found := objects[references[index].ObjectKey]; found {
			references[index].References = append(
				[]string(nil),
				object.References...,
			)
		}
	}

	entries := append([]ImageInfo(nil), references...)
	for key, object := range objects {
		if !referencedObjects[key] {
			entries = append(entries, cloneImageInfo(object))
		}
	}
	sort.Slice(entries, func(left, right int) bool {
		a := entries[left]
		b := entries[right]
		if a.Reference != b.Reference {
			if a.Reference == "" {
				return false
			}
			if b.Reference == "" {
				return true
			}
			return a.Reference < b.Reference
		}
		if platform := formatImagePlatform(a.Platform); platform !=
			formatImagePlatform(b.Platform) {
			return platform < formatImagePlatform(b.Platform)
		}
		return a.ID < b.ID
	})

	return imageCatalog{entries: entries, objects: objects}, nil
}

func (s *Store) readCatalogObjects(
	ctx context.Context,
) (result map[string]ImageInfo, returnErr error) {
	objectsDirectory, err := s.root.OpenDirectory("objects")
	if err != nil {
		return nil, fmt.Errorf("open OCI objects catalog: %w", err)
	}
	defer func() {
		s.logger.DebugError(
			"close OCI objects catalog",
			objectsDirectory.Close(),
		)
	}()

	platformNames, err := objectsDirectory.Names(imageCatalogEntries)
	if err != nil {
		return nil, fmt.Errorf("enumerate OCI object platforms: %w", err)
	}
	result = make(map[string]ImageInfo)
	for _, platformName := range platformNames {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		platformDirectory, err := objectsDirectory.OpenDirectory(platformName)
		if err != nil {
			continue
		}
		algorithmDirectory, err := platformDirectory.OpenDirectory("sha256")
		if err != nil {
			s.logger.DebugError(
				"close OCI platform catalog",
				platformDirectory.Close(),
				"platform",
				platformName,
			)
			continue
		}
		names, namesErr := algorithmDirectory.Names(imageCatalogEntries)
		closeErr := errors.Join(
			algorithmDirectory.Close(),
			platformDirectory.Close(),
		)
		s.logger.DebugError(
			"close OCI object catalog directories",
			closeErr,
			"platform",
			platformName,
		)
		if namesErr != nil {
			return nil, namesErr
		}
		for _, name := range names {
			key := filepath.Join(platformName, "sha256", name)
			result[key] = s.inspectCatalogObject(ctx, key)
		}
	}
	return result, nil
}

func (s *Store) inspectCatalogObject(
	ctx context.Context,
	key string,
) ImageInfo {
	info := ImageInfo{
		ID:           digest.SHA256.FromString(key).Encoded(),
		Kind:         ImageEntryObject,
		ObjectKey:    key,
		ObjectPath:   s.objectPath(key),
		MetadataPath: filepath.Join(s.objectPath(key), "metadata.json"),
		RootfsPath:   filepath.Join(s.objectPath(key), "bundle", "rootfs"),
	}
	components := strings.Split(filepath.ToSlash(key), "/")
	if len(components) == 3 &&
		components[1] == "sha256" &&
		validHexIdentifier(components[0]) &&
		validHexIdentifier(components[2]) {
		info.Manifest.Digest = digest.NewDigestFromEncoded(
			digest.SHA256,
			components[2],
		)
	} else {
		info.Problem = "OCI object path has an invalid identity shape"
		return info
	}

	prepared, found, err := s.retainObject(ctx, key, Metadata{})
	if err != nil {
		info.Problem = err.Error()
		return info
	}
	if !found {
		info.Problem = "OCI object rootfs is missing"
		return info
	}
	metadata := prepared.Metadata()
	rootfsPath, pathErr := s.resolvedPreparedRootfsPath(prepared)
	s.logger.DebugError("close inspected OCI object", prepared.Close())
	if pathErr != nil {
		info.Problem = pathErr.Error()
		return info
	}
	actualKey, err := immutableObjectKey(metadata.Spec)
	if err != nil || actualKey != key {
		info.Problem = errors.Join(
			fmt.Errorf("OCI object metadata identifies %q", actualKey),
			err,
		).Error()
		return info
	}

	info.Platform = metadata.Platform
	info.Manifest = metadata.Manifest
	info.Config = metadata.Config
	info.Runtime = metadata.Runtime
	info.RootfsPath = rootfsPath
	return info
}

func (s *Store) resolvedPreparedRootfsPath(
	prepared *Prepared,
) (path string, returnErr error) {
	rootfs, err := prepared.RootfsFile()
	if err != nil {
		return "", fmt.Errorf("duplicate OCI rootfs descriptor: %w", err)
	}
	defer func() {
		s.logger.DebugError(
			"close inspected OCI rootfs descriptor",
			rootfs.Close(),
		)
	}()

	path, err = os.Readlink(fmt.Sprintf("/proc/self/fd/%d", rootfs.Fd()))
	if err != nil {
		return "", fmt.Errorf("resolve OCI rootfs descriptor path: %w", err)
	}
	return path, nil
}

func (s *Store) readCatalogReferences(
	objects map[string]ImageInfo,
) (
	result []ImageInfo,
	referencedObjects map[string]bool,
	returnErr error,
) {
	referencesDirectory, err := s.root.OpenDirectory("references")
	if err != nil {
		return nil, nil, fmt.Errorf("open OCI references catalog: %w", err)
	}
	defer func() {
		s.logger.DebugError(
			"close OCI references catalog",
			referencesDirectory.Close(),
		)
	}()

	names, err := referencesDirectory.Names(imageCatalogEntries)
	if err != nil {
		return nil, nil, fmt.Errorf("enumerate OCI references: %w", err)
	}

	result = make([]ImageInfo, 0, len(names))
	referencedObjects = make(map[string]bool)
	for _, name := range names {
		info, object := inspectCatalogReference(
			referencesDirectory,
			name,
			objects,
		)
		if object != "" {
			referencedObjects[object] = true
			if current, found := objects[object]; found &&
				info.Reference != "" {
				current.References = append(
					current.References,
					info.Reference,
				)
				objects[object] = current
			}
		}
		result = append(result, info)
	}
	return result, referencedObjects, nil
}

func inspectCatalogReference(
	referencesDirectory interface {
		Path() string
		ReadFile(string, int64) ([]byte, error)
	},
	name string,
	objects map[string]ImageInfo,
) (ImageInfo, string) {
	id := strings.TrimSuffix(name, ".json")
	info := ImageInfo{
		ID:            id,
		Kind:          ImageEntryReference,
		ReferencePath: filepath.Join(referencesDirectory.Path(), name),
	}
	if name != id+".json" || !validHexIdentifier(id) {
		info.Problem = "OCI reference filename has an invalid identity"
		return info, ""
	}

	data, err := referencesDirectory.ReadFile(name, maximumMetadataBytes)
	if err != nil {
		info.Problem = fmt.Sprintf("read OCI reference metadata: %v", err)
		return info, ""
	}
	var record referenceRecord
	if err := decodeMetadata(data, &record); err != nil {
		info.Problem = fmt.Sprintf("decode OCI reference metadata: %v", err)
		return info, ""
	}
	info.Reference = record.Reference
	info.Platform = record.Platform
	info.ObjectKey = record.Object

	request, normalizeErr := normalizeRequest(Request{
		Reference: record.Reference,
		Platform:  record.Platform,
	})
	switch {
	case record.SchemaVersion != metadataSchemaVersion:
		info.Problem = fmt.Sprintf(
			"unsupported OCI reference metadata schema %d",
			record.SchemaVersion,
		)
	case normalizeErr != nil:
		info.Problem = normalizeErr.Error()
	case request.key != id:
		info.Problem = fmt.Sprintf(
			"OCI reference metadata identifies record %q",
			request.key,
		)
	case record.Object == "" || !filepath.IsLocal(record.Object):
		info.Problem = "OCI reference object identity is invalid"
	}

	object, found := objects[record.Object]
	if !found {
		if info.Problem == "" {
			info.Problem = "referenced OCI object is missing"
		}
		return info, record.Object
	}
	info.Manifest = object.Manifest
	info.Config = object.Config
	info.Runtime = object.Runtime
	info.ObjectPath = object.ObjectPath
	info.MetadataPath = object.MetadataPath
	info.RootfsPath = object.RootfsPath
	if info.Problem == "" && object.Problem != "" {
		info.Problem = object.Problem
	}
	return info, record.Object
}

func resolveObjectDigest(
	selector string,
	platform ocispec.Platform,
	objects map[string]ImageInfo,
) (ImageInfo, error) {
	value := digest.Digest(selector)
	if err := value.Validate(); err != nil ||
		value.Algorithm() != digest.SHA256 {
		return ImageInfo{}, fmt.Errorf(
			"invalid OCI image digest %q",
			selector,
		)
	}
	var matches []ImageInfo
	for _, object := range objects {
		if object.Manifest.Digest == value &&
			samePlatform(object.Platform, platform) {
			matches = append(matches, object)
		}
	}
	return singleImageMatch(selector, matches)
}

func resolveImageIDPrefix(
	selector string,
	platform ocispec.Platform,
	catalog imageCatalog,
) (ImageInfo, bool, error) {
	if len(selector) < imageIDShortLength {
		return ImageInfo{}, false, fmt.Errorf(
			"image ID prefix must contain at least %d hexadecimal characters",
			imageIDShortLength,
		)
	}
	var matches []ImageInfo
	for _, entry := range catalog.entries {
		if strings.HasPrefix(entry.ID, selector) {
			matches = append(matches, entry)
		}
	}
	for _, object := range catalog.objects {
		if strings.HasPrefix(object.ID, selector) ||
			(strings.HasPrefix(
				object.Manifest.Digest.Encoded(),
				selector,
			) && samePlatform(object.Platform, platform)) {
			matches = append(matches, object)
		}
	}
	matches = uniqueImageMatches(matches)
	switch len(matches) {
	case 0:
		return ImageInfo{}, false, nil
	case 1:
		return cloneImageInfo(matches[0]), true, nil
	default:
		return ImageInfo{}, false, fmt.Errorf(
			"OCI image selector %q is ambiguous",
			selector,
		)
	}
}

func singleImageMatch(
	selector string,
	matches []ImageInfo,
) (ImageInfo, error) {
	switch len(matches) {
	case 0:
		return ImageInfo{}, fmt.Errorf(
			"OCI image %q does not exist",
			selector,
		)
	case 1:
		return cloneImageInfo(matches[0]), nil
	default:
		return ImageInfo{}, fmt.Errorf(
			"OCI image selector %q is ambiguous",
			selector,
		)
	}
}

func uniqueImageMatches(input []ImageInfo) []ImageInfo {
	result := make([]ImageInfo, 0, len(input))
	seen := make(map[string]bool)
	for _, info := range input {
		key := string(info.Kind) + "\x00" + info.ID
		if !seen[key] {
			seen[key] = true
			result = append(result, info)
		}
	}
	return result
}

func defaultImagePlatform(platform ocispec.Platform) ocispec.Platform {
	if platform.OS == "" {
		platform.OS = "linux"
	}
	if platform.Architecture == "" {
		platform.Architecture = runtime.GOARCH
	}
	return platform
}

func formatImagePlatform(platform ocispec.Platform) string {
	if platform.OS == "" && platform.Architecture == "" {
		return "-"
	}
	value := platform.OS + "/" + platform.Architecture
	if platform.Variant != "" {
		value += "/" + platform.Variant
	}
	return value
}

func isHexPrefix(value string) bool {
	if len(value) < imageIDShortLength {
		return false
	}
	for _, character := range value {
		if character < '0' ||
			(character > '9' && character < 'a') ||
			character > 'f' {
			return false
		}
	}
	return true
}

func validHexIdentifier(value string) bool {
	return len(value) == 64 && isHexPrefix(value)
}

func validateImageContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("OCI image operation context is nil")
	}
	return ctx.Err()
}
