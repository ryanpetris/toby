//go:build linux

package caddy

// Resolves opted-in Caddy integration tests to the production image by default,
// with an environment override for registry-specific test environments.

import (
	"os"
	"testing"
)

func integrationImage(t *testing.T) string {
	t.Helper()

	value := os.Getenv("TOBY_CADDY_OCI_IMAGE")
	if value == "" {
		value = DefaultImage
	}

	normalized, err := normalizeImage(value)
	if err != nil {
		t.Fatal(err)
	}

	return normalized
}
