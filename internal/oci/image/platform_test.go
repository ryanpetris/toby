package image

// Covers shared OCI platform canonicalization and detached feature storage.

import (
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func TestNormalizePlatform(t *testing.T) {
	features := []string{"z", "a"}
	platform, err := NormalizePlatform(ocispec.Platform{
		OS:           " linux ",
		Architecture: " amd64 ",
		Variant:      " v1 ",
		OSVersion:    " version ",
		OSFeatures:   features,
	})
	if err != nil {
		t.Fatal(err)
	}
	features[0] = "changed"
	if platform.OS != "linux" ||
		platform.Architecture != "amd64" ||
		platform.Variant != "v1" ||
		platform.OSVersion != "version" ||
		len(platform.OSFeatures) != 2 ||
		platform.OSFeatures[0] != "a" ||
		platform.OSFeatures[1] != "z" {
		t.Fatalf("platform = %#v", platform)
	}
}

func TestNormalizePlatformRejectsIncompleteAndNULValues(t *testing.T) {
	for _, platform := range []ocispec.Platform{
		{OS: "linux"},
		{Architecture: "amd64"},
		{OS: "linux", Architecture: "amd64\x00"},
		{
			OS:           "linux",
			Architecture: "amd64",
			OSFeatures:   []string{"feature\x00"},
		},
	} {
		if _, err := NormalizePlatform(platform); err == nil {
			t.Errorf("NormalizePlatform(%#v) succeeded", platform)
		}
	}
}
