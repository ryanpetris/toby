package sidecar

// Defines secret-bearing launch input and safe immutable image metadata.

import (
	"fmt"
	"path/filepath"
	"strings"

	"petris.dev/toby/internal/agent/resource"
	"petris.dev/toby/internal/mcpgateway"
	"petris.dev/toby/internal/sandbox/mount"
)

// Definition is one complete local MCP sidecar launch.
type Definition struct {
	Image       string
	Command     []string
	Environment map[string]string
	Mounts      []mcpgateway.Mount
	Network     resource.Network
}

var _ fmt.Stringer = Definition{}

// String withholds all launch details.
func (Definition) String() string {
	return "{Image:<redacted> Command:<redacted> Environment:<redacted> Mounts:<redacted> Network:<redacted>}"
}

// Metadata contains only immutable values needed to construct a canonical
// agent resource identity. String still redacts it to keep diagnostics
// uniform.
type Metadata struct {
	ImmutableImage string
	ManifestDigest string
	RootFSDigest   string
	Workdir        string
}

var _ fmt.Stringer = Metadata{}

// String withholds image and process identity details.
func (Metadata) String() string {
	return "{ImmutableImage:<redacted> ManifestDigest:<redacted> RootFSDigest:<redacted> Workdir:<redacted>}"
}

func cloneDefinition(definition Definition) Definition {
	clone := definition
	clone.Command = append([]string(nil), definition.Command...)
	clone.Mounts = append(
		[]mcpgateway.Mount(nil),
		definition.Mounts...,
	)
	clone.Environment = make(
		map[string]string,
		len(definition.Environment),
	)
	for name, value := range definition.Environment {
		clone.Environment[name] = value
	}

	return clone
}

func validateDefinition(definition Definition) error {
	if definition.Image == "" {
		return fmt.Errorf("sidecar image is required")
	}
	if len(definition.Command) == 0 || definition.Command[0] == "" {
		return fmt.Errorf("sidecar command is required")
	}
	for _, argument := range definition.Command {
		if strings.ContainsRune(argument, 0) {
			return fmt.Errorf("sidecar command contains a NUL byte")
		}
	}
	for name, value := range definition.Environment {
		if name == "" ||
			strings.ContainsAny(name, "=\x00") ||
			strings.ContainsRune(value, 0) {
			return fmt.Errorf("sidecar environment is invalid")
		}
	}
	switch definition.Network {
	case resource.NetworkHost, resource.NetworkPrivate:
	default:
		return fmt.Errorf(
			"sidecar network mode %q is unsupported",
			definition.Network,
		)
	}
	return validateMounts(definition.Mounts)
}

func validateMounts(definition []mcpgateway.Mount) error {
	for index, item := range definition {
		if item.Source == "" ||
			!filepath.IsAbs(item.Source) ||
			filepath.Clean(item.Source) != item.Source ||
			strings.ContainsRune(item.Source, 0) {
			return fmt.Errorf(
				"sidecar mount %d source is invalid",
				index,
			)
		}
		if err := mount.ValidateTarget(item.Target); err != nil {
			return fmt.Errorf("sidecar mount %d: %w", index, err)
		}
		if err := item.Access.Validate(); err != nil {
			return fmt.Errorf("sidecar mount %d: %w", index, err)
		}
		for _, reserved := range []string{
			"/proc",
			"/dev",
			"/tmp",
			"/run",
		} {
			if mount.TargetsOverlap(item.Target, reserved) {
				return fmt.Errorf(
					"sidecar mount %d target overlaps reserved path %q",
					index,
					reserved,
				)
			}
		}
		switch item.Scope {
		case resource.ScopeHome, resource.ScopeProject:
		default:
			return fmt.Errorf(
				"sidecar mount %d scope %q is unsupported",
				index,
				item.Scope,
			)
		}
		for earlier := range index {
			if mount.TargetsOverlap(
				definition[earlier].Target,
				item.Target,
			) {
				return fmt.Errorf(
					"sidecar mounts %d and %d overlap",
					earlier,
					index,
				)
			}
		}
	}

	return nil
}
