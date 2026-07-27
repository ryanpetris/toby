package oci

// Describes cached OCI reference records and deduplicated immutable objects.

import (
	"fmt"
	"sort"
	"strings"

	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"petris.dev/toby/internal/oci/image"
)

const imageIDShortLength = 12

// ImageEntryKind distinguishes a mutable reference mapping from an immutable
// object selected directly by its identity.
type ImageEntryKind string

const (
	// ImageEntryReference identifies a mutable image name.
	ImageEntryReference ImageEntryKind = "reference"
	// ImageEntryObject identifies immutable stored image content.
	ImageEntryObject ImageEntryKind = "object"
)

// ImageInfo is one catalog view. Reference entries carry Reference and
// ReferencePath; object entries represent dangling or directly selected
// immutable objects.
type ImageInfo struct {
	ID            string
	Kind          ImageEntryKind
	Reference     string
	Platform      ocispec.Platform
	Manifest      ocispec.Descriptor
	Config        ocispec.Descriptor
	Runtime       RuntimeConfig
	References    []string
	ObjectKey     string
	ReferencePath string
	ObjectPath    string
	MetadataPath  string
	RootfsPath    string
	Problem       string
}

// ShortID returns the display prefix accepted by image selectors.
func (i ImageInfo) ShortID() string {
	if len(i.ID) <= imageIDShortLength {
		return i.ID
	}
	return i.ID[:imageIDShortLength]
}

// ImageID returns the display prefix of the manifest digest.
func (i ImageInfo) ImageID() string {
	value := i.Manifest.Digest.Encoded()
	if len(value) <= imageIDShortLength {
		return value
	}
	return value[:imageIDShortLength]
}

// Dangling reports whether an object has no published references.
func (i ImageInfo) Dangling() bool {
	return i.Kind == ImageEntryObject && len(i.References) == 0
}

// ImageFilter selects catalog rows whose nonempty fields match exactly.
type ImageFilter struct {
	Reference string
	Platform  ocispec.Platform
	Digest    digest.Digest
	Dangling  *bool
}

func (f ImageFilter) normalize() (ImageFilter, error) {
	result := f
	if strings.TrimSpace(result.Reference) != "" {
		reference, err := image.NormalizeReference(result.Reference)
		if err != nil {
			return ImageFilter{}, err
		}
		result.Reference = reference
	}
	if result.Platform.OS != "" ||
		result.Platform.Architecture != "" ||
		result.Platform.Variant != "" {
		platform, err := image.NormalizePlatform(result.Platform)
		if err != nil {
			return ImageFilter{}, err
		}
		result.Platform = platform
	}
	if result.Digest != "" {
		if err := result.Digest.Validate(); err != nil {
			return ImageFilter{}, fmt.Errorf(
				"invalid OCI manifest digest: %w",
				err,
			)
		}
		if result.Digest.Algorithm() != digest.SHA256 {
			return ImageFilter{}, fmt.Errorf(
				"unsupported OCI manifest digest algorithm %q",
				result.Digest.Algorithm(),
			)
		}
	}
	return result, nil
}

func (f ImageFilter) matches(info ImageInfo) bool {
	if f.Reference != "" && info.Reference != f.Reference {
		return false
	}
	if f.Platform.OS != "" &&
		!samePlatform(f.Platform, info.Platform) {
		return false
	}
	if f.Digest != "" && info.Manifest.Digest != f.Digest {
		return false
	}
	if f.Dangling != nil && info.Dangling() != *f.Dangling {
		return false
	}
	return true
}

func samePlatform(left, right ocispec.Platform) bool {
	if left.OS != right.OS ||
		left.Architecture != right.Architecture ||
		left.Variant != right.Variant ||
		left.OSVersion != right.OSVersion ||
		len(left.OSFeatures) != len(right.OSFeatures) {
		return false
	}
	leftFeatures := append([]string(nil), left.OSFeatures...)
	rightFeatures := append([]string(nil), right.OSFeatures...)
	sort.Strings(leftFeatures)
	sort.Strings(rightFeatures)
	for index := range leftFeatures {
		if leftFeatures[index] != rightFeatures[index] {
			return false
		}
	}
	return true
}

func cloneImageInfo(info ImageInfo) ImageInfo {
	clone := info
	clone.Platform.OSFeatures = append(
		[]string(nil),
		info.Platform.OSFeatures...,
	)
	clone.Manifest = cloneDescriptor(info.Manifest)
	clone.Config = cloneDescriptor(info.Config)
	clone.Runtime = cloneSpec(Spec{Runtime: info.Runtime}).Runtime
	clone.References = append([]string(nil), info.References...)
	return clone
}
