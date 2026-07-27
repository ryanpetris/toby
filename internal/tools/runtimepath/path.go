// Package runtimepath validates and resolves tool runtime assets beneath
// Bubblewrap's transient runtime directory.
package runtimepath

import (
	"fmt"
	"path"
	"strings"

	"petris.dev/toby/internal/sandbox/layout"
)

// Resolve returns the native Bubblewrap runtime path for relative.
func Resolve(relative string) (string, error) {
	if err := validateRelative(relative); err != nil {
		return "", err
	}
	return path.Join(layout.Runtime, relative), nil
}

func validateRelative(relative string) error {
	if relative == "" ||
		path.IsAbs(relative) ||
		path.Clean(relative) != relative ||
		relative == ".." ||
		strings.HasPrefix(relative, "../") ||
		strings.ContainsRune(relative, 0) {
		return fmt.Errorf("runtime asset path must be clean and relative: %q", relative)
	}

	return nil
}
