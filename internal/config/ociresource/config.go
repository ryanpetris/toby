package ociresource

// Defines OCI image resource defaulting and validation.

import (
	"fmt"
	"runtime"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"petris.dev/toby/internal/oci/image"
)

// Config identifies one image preparation resource.
type Config struct {
	Reference  string           `json:"reference"`
	Platform   ocispec.Platform `json:"platform"`
	PullPolicy image.PullPolicy `json:"pull_policy"`
}

// Normalize applies platform and pull-policy defaults and validates the
// effective image request.
func Normalize(input Config) (Config, error) {
	result := input
	reference, err := image.NormalizeReference(result.Reference)
	if err != nil {
		return Config{}, err
	}
	result.Reference = reference
	if result.Platform.OS == "" {
		result.Platform.OS = "linux"
	}
	if result.Platform.Architecture == "" {
		result.Platform.Architecture = runtime.GOARCH
	}
	platform, err := image.NormalizePlatform(result.Platform)
	if err != nil {
		return Config{}, err
	}
	result.Platform = platform
	if result.PullPolicy == "" {
		result.PullPolicy = image.PullIfMissing
	}

	if result.Platform.OS != "linux" {
		return Config{}, fmt.Errorf(
			"OCI resource platform OS %q is unsupported",
			result.Platform.OS,
		)
	}
	switch result.PullPolicy {
	case image.PullIfMissing, image.PullAlways, image.PullNever:
	default:
		return Config{}, fmt.Errorf(
			"OCI resource pull policy %q is unsupported",
			result.PullPolicy,
		)
	}

	return result, nil
}
