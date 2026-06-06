// Package containerconfig holds the shared `container:` config block — the
// container image and an optional build that produces it — decoded and resolved
// identically by both the host config and the launch config.
package containerconfig

import (
	"fmt"
	"path/filepath"
	"strings"

	"petris.dev/toby/config"
	"petris.dev/toby/tools"
)

// Config is the decoded `container:` block.
type Config struct {
	Image string `json:"image" yaml:"image"`
	Build *Build `json:"build" yaml:"build"`
}

// Build is the decoded `container.build` block. Context and Dockerfile are
// pointers so an absent key (use the default) is distinguished from an explicit
// empty string (an error).
type Build struct {
	Context    *string `json:"context" yaml:"context"`
	Dockerfile *string `json:"dockerfile" yaml:"dockerfile"`
}

// ResolveBuild resolves a decoded build block into a tools.Build, applying the
// "." context and "Dockerfile" defaults and anchoring relative paths at the
// config file's directory (dir), with ~ expanded against home. A nil block
// resolves to a zero (unset) tools.Build.
func ResolveBuild(b *Build, dir, home string) (tools.Build, error) {
	if b == nil {
		return tools.Build{}, nil
	}
	context, err := resolveField(b.Context, ".", "container.build.context")
	if err != nil {
		return tools.Build{}, err
	}
	dockerfile, err := resolveField(b.Dockerfile, "Dockerfile", "container.build.dockerfile")
	if err != nil {
		return tools.Build{}, err
	}
	contextDir, err := resolvePath(context, dir, home)
	if err != nil {
		return tools.Build{}, fmt.Errorf("container.build.context: %w", err)
	}
	file := config.ExpandHome(dockerfile, home)
	if !filepath.IsAbs(file) {
		file = filepath.Join(contextDir, file)
	}
	return tools.Build{Context: contextDir, Dockerfile: file}, nil
}

func resolveField(value *string, fallback, label string) (string, error) {
	if value == nil {
		return fallback, nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return "", fmt.Errorf("%s must not be empty", label)
	}
	return trimmed, nil
}

func resolvePath(value, dir, home string) (string, error) {
	value = config.ExpandHome(value, home)
	if filepath.IsAbs(value) {
		return value, nil
	}
	return filepath.Join(dir, value), nil
}
