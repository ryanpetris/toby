package image

// Normalizes registry image references before resource hashing and storage.

import (
	"fmt"
	"strings"

	distref "github.com/distribution/reference"
)

// NormalizeReference returns the canonical tagged or digest reference used by
// both agent resource identities and the per-user image store.
func NormalizeReference(input string) (string, error) {
	value := strings.TrimSpace(input)
	if value == "" {
		return "", fmt.Errorf("OCI image reference must not be empty")
	}
	if strings.ContainsRune(value, 0) {
		return "", fmt.Errorf("OCI image reference contains a NUL byte")
	}

	named, err := distref.ParseNormalizedNamed(value)
	if err != nil {
		return "", fmt.Errorf(
			"parse OCI image reference %q: %w",
			value,
			err,
		)
	}

	return distref.TagNameOnly(named).String(), nil
}

// Repository returns the canonical repository name without a tag or digest.
func Repository(reference string) (string, error) {
	normalized, err := NormalizeReference(reference)
	if err != nil {
		return "", err
	}
	named, err := distref.ParseNormalizedNamed(normalized)
	if err != nil {
		return "", fmt.Errorf(
			"parse normalized OCI image reference %q: %w",
			normalized,
			err,
		)
	}

	return named.Name(), nil
}
