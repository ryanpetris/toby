package image

// Canonicalizes OCI platform values shared by resource hashing and storage
// identities.

import (
	"fmt"
	"sort"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// NormalizePlatform trims platform identity fields, copies feature slices,
// and rejects incomplete or NUL-containing identities.
func NormalizePlatform(
	input ocispec.Platform,
) (ocispec.Platform, error) {
	platform := input
	platform.OS = strings.TrimSpace(platform.OS)
	platform.Architecture = strings.TrimSpace(platform.Architecture)
	platform.Variant = strings.TrimSpace(platform.Variant)
	platform.OSVersion = strings.TrimSpace(platform.OSVersion)
	platform.OSFeatures = append([]string(nil), platform.OSFeatures...)
	sort.Strings(platform.OSFeatures)

	if platform.OS == "" || platform.Architecture == "" {
		return ocispec.Platform{}, fmt.Errorf(
			"OCI platform OS and architecture are required",
		)
	}
	for _, value := range append(
		[]string{
			platform.OS,
			platform.Architecture,
			platform.Variant,
			platform.OSVersion,
		},
		platform.OSFeatures...,
	) {
		if strings.ContainsRune(value, 0) {
			return ocispec.Platform{}, fmt.Errorf(
				"OCI platform contains a NUL byte",
			)
		}
	}

	return platform, nil
}
