package storage

// Defines user-facing volume specifications and exact metadata filters.

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"petris.dev/toby/internal/sandbox/mount"
)

// VolumeType identifies one persistent-volume metadata shape.
type VolumeType string

const (
	// VolumeTypeHome stores one private home.
	VolumeTypeHome VolumeType = "home"
	// VolumeTypeTool stores one tool-owned persistent directory.
	VolumeTypeTool VolumeType = "tool"
)

// VolumeSpec completely identifies one persistent volume. An empty profile
// selects the default profile.
type VolumeSpec struct {
	Type    VolumeType
	Name    string
	Profile string
	Purpose string
}

// VolumeFilter selects volumes whose nonempty fields match exactly.
type VolumeFilter struct {
	Type    VolumeType
	Name    string
	Profile string
	Purpose string
}

// Normalize applies the default profile and validates the complete identity.
func (s VolumeSpec) Normalize() (VolumeSpec, error) {
	spec, _, err := normalizeVolumeSpec(s)
	return spec, err
}

// Normalize validates and canonicalizes the fields used for exact matching.
func (f VolumeFilter) Normalize() (VolumeFilter, error) {
	return normalizeVolumeFilter(f)
}

func normalizeVolumeSpec(input VolumeSpec) (VolumeSpec, volumeMetadata, error) {
	spec := input
	spec.Type = VolumeType(strings.TrimSpace(string(spec.Type)))
	spec.Profile = strings.TrimSpace(spec.Profile)
	if spec.Profile == "" {
		spec.Profile = defaultProfile
	}

	switch spec.Type {
	case VolumeTypeHome:
		if spec.Purpose != "" {
			return VolumeSpec{}, volumeMetadata{},
				fmt.Errorf("home volume purpose must be empty")
		}
		if _, err := ResolveHomeIdentity(spec.Name, spec.Profile); err != nil {
			return VolumeSpec{}, volumeMetadata{}, err
		}
		return spec, newHomeMetadata(spec.Profile, spec.Name), nil
	case VolumeTypeTool:
		key := mount.Key{
			Type:    mount.TypeTool,
			Name:    spec.Name,
			Purpose: spec.Purpose,
		}
		if err := key.Validate(); err != nil {
			return VolumeSpec{}, volumeMetadata{},
				fmt.Errorf("tool volume specification: %w", err)
		}
		return spec,
			newToolMetadata(spec.Profile, spec.Name, spec.Purpose),
			nil
	case "":
		return VolumeSpec{}, volumeMetadata{},
			fmt.Errorf("volume type must not be empty")
	default:
		return VolumeSpec{}, volumeMetadata{},
			fmt.Errorf("unsupported volume type %q", spec.Type)
	}
}

func normalizeVolumeFilter(input VolumeFilter) (VolumeFilter, error) {
	filter := input
	filter.Type = VolumeType(strings.TrimSpace(string(filter.Type)))
	filter.Profile = strings.TrimSpace(filter.Profile)
	if input.Type != "" && filter.Type == "" {
		return VolumeFilter{}, fmt.Errorf("volume type filter must not be blank")
	}
	if input.Profile != "" && filter.Profile == "" {
		return VolumeFilter{},
			fmt.Errorf("volume profile filter must not be blank")
	}

	switch filter.Type {
	case "", VolumeTypeHome, VolumeTypeTool:
	default:
		return VolumeFilter{},
			fmt.Errorf("unsupported volume type %q", filter.Type)
	}
	for _, field := range [][2]string{
		{"name", filter.Name},
		{"profile", filter.Profile},
		{"purpose", filter.Purpose},
	} {
		label, value := field[0], field[1]
		if !utf8.ValidString(value) {
			return VolumeFilter{},
				fmt.Errorf("volume %s filter is not valid UTF-8", label)
		}
	}
	if filter.Type == VolumeTypeHome && filter.Purpose != "" {
		return VolumeFilter{},
			fmt.Errorf("home volumes do not have a purpose")
	}
	return filter, nil
}

func (f VolumeFilter) matches(info VolumeInfo) bool {
	return (f.Type == "" || f.Type == info.Type) &&
		(f.Name == "" || f.Name == info.Name) &&
		(f.Profile == "" || f.Profile == info.Profile) &&
		(f.Purpose == "" || f.Purpose == info.Purpose)
}
