package config

// Validates the configured XDG runtime path shape before Toby derives agent
// and per-run paths from it.

import (
	"fmt"
	"path/filepath"
)

func validateRuntimeDir(path string) error {
	if path == "" {
		return fmt.Errorf("XDG_RUNTIME_DIR is required for sandbox and agent operations")
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("XDG_RUNTIME_DIR must be a clean absolute path: %q", path)
	}

	return nil
}
