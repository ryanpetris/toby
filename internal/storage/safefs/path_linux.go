//go:build linux

package safefs

// Validates Linux absolute roots and lexical relative capability paths.

import (
	"fmt"
	"path/filepath"
	"strings"
)

func validateAbsolutePath(name string) ([]string, error) {
	if name == "" || strings.IndexByte(name, 0) >= 0 || !filepath.IsAbs(name) {
		return nil, fmt.Errorf("%w: root %q must be an absolute path", ErrUnsafePath, name)
	}
	if filepath.Clean(name) != name {
		return nil, fmt.Errorf("%w: root %q is not clean", ErrUnsafePath, name)
	}
	if name == string(filepath.Separator) {
		return nil, nil
	}

	components := strings.Split(strings.TrimPrefix(name, string(filepath.Separator)), string(filepath.Separator))
	for _, component := range components {
		if component == "" || component == "." || component == ".." {
			return nil, fmt.Errorf("%w: root %q contains an invalid component", ErrUnsafePath, name)
		}
	}
	return components, nil
}

func validateRelativePath(name string) ([]string, error) {
	if name == "" || strings.IndexByte(name, 0) >= 0 || filepath.IsAbs(name) {
		return nil, fmt.Errorf("%w: %q must be a non-empty relative path", ErrUnsafePath, name)
	}

	components := strings.Split(name, string(filepath.Separator))
	for _, component := range components {
		if component == "" || component == "." || component == ".." {
			return nil, fmt.Errorf("%w: relative path %q contains an invalid component", ErrUnsafePath, name)
		}
	}
	return components, nil
}

func splitParent(name string) (string, string, error) {
	components, err := validateRelativePath(name)
	if err != nil {
		return "", "", err
	}
	if len(components) == 1 {
		return "", components[0], nil
	}
	return strings.Join(components[:len(components)-1], string(filepath.Separator)), components[len(components)-1], nil
}
