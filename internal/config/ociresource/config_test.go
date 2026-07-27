package ociresource

// Covers canonical defaults shared by agent OCI resource identities.

import (
	"runtime"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"petris.dev/toby/internal/oci/image"
)

func TestNormalizeCanonicalizesDefaultsReferenceAndPlatform(t *testing.T) {
	config, err := Normalize(Config{
		Reference: " alpine ",
		Platform: ocispec.Platform{
			OS:           " linux ",
			Architecture: " " + runtime.GOARCH + " ",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.Reference != "docker.io/library/alpine:latest" ||
		config.Platform.OS != "linux" ||
		config.Platform.Architecture != runtime.GOARCH ||
		config.PullPolicy != image.PullIfMissing {
		t.Fatalf("config = %#v", config)
	}
}

func TestNormalizeRejectsUnsupportedPlatform(t *testing.T) {
	if _, err := Normalize(Config{
		Reference: "alpine",
		Platform: ocispec.Platform{
			OS:           "darwin",
			Architecture: "amd64",
		},
	}); err == nil {
		t.Fatal("unsupported platform succeeded")
	}
}
