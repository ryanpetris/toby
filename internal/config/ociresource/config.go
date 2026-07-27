package ociresource

// Defines OCI image resource defaulting and validation.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"petris.dev/toby/internal/oci/image"
	"petris.dev/toby/internal/oci/imagesource"
)

// Config identifies one image preparation resource.
type Config struct {
	Source     imagesource.Kind        `json:"source"`
	Reference  string                  `json:"reference"`
	Archive    string                  `json:"archive,omitempty"`
	Build      imagesource.BuildConfig `json:"build,omitempty"`
	Profile    string                  `json:"profile,omitempty"`
	Project    string                  `json:"project,omitempty"`
	Platform   ocispec.Platform        `json:"platform"`
	PullPolicy image.PullPolicy        `json:"pull_policy"`
}

// Normalize applies platform and pull-policy defaults and validates the
// effective image request.
func Normalize(input Config) (Config, error) {
	result := input
	if result.Source == "" {
		result.Source = imagesource.Registry
	}
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

	if result.Platform.OS != "linux" {
		return Config{}, fmt.Errorf(
			"OCI resource platform OS %q is unsupported",
			result.Platform.OS,
		)
	}
	if result.PullPolicy == "" {
		result.PullPolicy = image.PullIfMissing
	}
	switch result.PullPolicy {
	case image.PullIfMissing, image.PullAlways, image.PullNever:
	default:
		return Config{}, fmt.Errorf(
			"OCI resource pull policy %q is unsupported",
			result.PullPolicy,
		)
	}

	switch result.Source {
	case imagesource.Registry:
		if strings.TrimSpace(result.Archive) != "" ||
			result.Build != (imagesource.BuildConfig{}) ||
			strings.TrimSpace(result.Profile) != "" ||
			strings.TrimSpace(result.Project) != "" {
			return Config{}, fmt.Errorf(
				"registry OCI resource must not configure an archive, build, profile, or project",
			)
		}
		reference, err := image.NormalizeReference(result.Reference)
		if err != nil {
			return Config{}, err
		}
		result.Reference = reference
	case imagesource.Archive:
		if result.Build != (imagesource.BuildConfig{}) ||
			strings.TrimSpace(result.Profile) != "" ||
			strings.TrimSpace(result.Project) != "" {
			return Config{}, fmt.Errorf(
				"archive OCI resource must not configure a build, profile, or project",
			)
		}
		archive, err := normalizeAbsolutePath(
			result.Archive,
			"OCI archive path",
		)
		if err != nil {
			return Config{}, err
		}
		result.Archive = archive
		if err := normalizeLocalReference(&result); err != nil {
			return Config{}, err
		}
	case imagesource.Build:
		if strings.TrimSpace(result.Archive) != "" {
			return Config{}, fmt.Errorf(
				"build OCI resource must not configure an archive",
			)
		}
		contextPath, err := normalizeAbsolutePath(
			result.Build.Context,
			"OCI build context",
		)
		if err != nil {
			return Config{}, err
		}
		dockerfile, err := normalizeAbsolutePath(
			result.Build.Dockerfile,
			"OCI build Dockerfile",
		)
		if err != nil {
			return Config{}, err
		}
		result.Build = imagesource.BuildConfig{
			Context:    contextPath,
			Dockerfile: dockerfile,
		}
		result.Profile, err = normalizeBuildName(
			result.Profile,
			"OCI build profile",
		)
		if err != nil {
			return Config{}, err
		}
		result.Project, err = normalizeBuildName(
			result.Project,
			"OCI build project",
		)
		if err != nil {
			return Config{}, err
		}
		if err := normalizeLocalReference(&result); err != nil {
			return Config{}, err
		}
	default:
		return Config{}, fmt.Errorf(
			"OCI resource source %q is unsupported",
			result.Source,
		)
	}

	return result, nil
}

func normalizeAbsolutePath(value string, label string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s must not be empty", label)
	}
	if strings.ContainsRune(value, 0) {
		return "", fmt.Errorf("%s contains a NUL byte", label)
	}
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("%s must be absolute", label)
	}
	return filepath.Clean(value), nil
}

func normalizeBuildName(value string, label string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s must not be empty", label)
	}
	if !utf8.ValidString(value) {
		return "", fmt.Errorf("%s is not valid UTF-8", label)
	}
	if strings.ContainsRune(value, 0) {
		return "", fmt.Errorf("%s contains a NUL byte", label)
	}
	return value, nil
}

func normalizeLocalReference(config *Config) error {
	reference := strings.TrimSpace(config.Reference)
	sum, err := localSourceSum(*config)
	if err != nil {
		return err
	}
	digest := hex.EncodeToString(sum[:])
	if config.Source == imagesource.Build {
		reference = "toby.local/" +
			localReferenceComponent(config.Profile) +
			"/" +
			localReferenceComponent(config.Project) +
			":" +
			digest
	} else if reference == "" {
		reference = "toby.local/" +
			string(config.Source) +
			"/" +
			digest +
			":latest"
	}

	normalized, err := image.NormalizeReference(reference)
	if err != nil {
		return err
	}
	config.Reference = normalized
	return nil
}

func localSourceSum(config Config) ([sha256.Size]byte, error) {
	document := struct {
		Source   imagesource.Kind        `json:"source"`
		Archive  string                  `json:"archive,omitempty"`
		Build    imagesource.BuildConfig `json:"build,omitempty"`
		Platform ocispec.Platform        `json:"platform"`
	}{
		Source:   config.Source,
		Archive:  config.Archive,
		Build:    config.Build,
		Platform: config.Platform,
	}
	data, err := json.Marshal(document)
	if err != nil {
		return [sha256.Size]byte{}, fmt.Errorf(
			"encode local OCI source identity: %w",
			err,
		)
	}
	return sha256.Sum256(data), nil
}

func localReferenceComponent(value string) string {
	const (
		maxDirectLength = 64
		maxSlugLength   = 48
		hashLength      = 12
	)

	if len(value) <= maxDirectLength {
		candidate := "toby.local/component/" + value + ":tag"
		if normalized, err := image.NormalizeReference(candidate); err == nil &&
			normalized == candidate {
			return value
		}
	}

	var slug strings.Builder
	separator := false
	for _, current := range value {
		switch {
		case current >= 'a' && current <= 'z',
			current >= '0' && current <= '9':
			if separator && slug.Len() != 0 {
				slug.WriteByte('-')
			}
			separator = false
			if slug.Len() < maxSlugLength {
				slug.WriteRune(current)
			}
		case current >= 'A' && current <= 'Z':
			if separator && slug.Len() != 0 {
				slug.WriteByte('-')
			}
			separator = false
			if slug.Len() < maxSlugLength {
				slug.WriteRune(current + ('a' - 'A'))
			}
		default:
			separator = true
		}
	}
	safe := strings.Trim(slug.String(), "-")
	if safe == "" {
		safe = "name"
	}
	sum := sha256.Sum256([]byte(value))
	return safe + "-" + hex.EncodeToString(sum[:])[:hashLength]
}
