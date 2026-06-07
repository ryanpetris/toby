package warning

// Warning identity and suppression config: the registered warning IDs (with
// parsing/validation) and the Suppression set recording which IDs — or all of
// them — the user has silenced, with merge/clone helpers for layered config.

import (
	"fmt"
	"strings"
)

type ID string

const (
	MountHostBacking        ID = "mount.host-backing"
	ModelDiscovery          ID = "provider.model-discovery"
	ProjectAutoloadDisabled ID = "project.autoload-disabled"
	ProjectDuplicate        ID = "project.duplicate"
	ProjectMissing          ID = "project.missing"
	PermissionAutoDeny      ID = "permission.auto-deny"
)

func ParseID(value string) (ID, error) {
	switch id := ID(strings.TrimSpace(value)); id {
	case MountHostBacking, ModelDiscovery, ProjectAutoloadDisabled, ProjectDuplicate, ProjectMissing, PermissionAutoDeny:
		return id, nil
	default:
		return "", fmt.Errorf("warning id must be one of %q, %q, %q, %q, %q, or %q", MountHostBacking, ModelDiscovery, ProjectAutoloadDisabled, ProjectDuplicate, ProjectMissing, PermissionAutoDeny)
	}
}

type Suppression struct {
	Set bool
	All bool
	IDs map[ID]bool
}

// SuppressionFromList builds a Suppression from the list form of
// settings.suppressWarnings. The single entry "*" suppresses every warning; any
// other entry must be a registered warning ID.
func SuppressionFromList(list []string, label string) (Suppression, error) {
	result := Suppression{Set: true}
	ids := map[ID]bool{}
	for i, item := range list {
		if strings.TrimSpace(item) == "*" {
			result.All = true
			continue
		}
		id, err := ParseID(item)
		if err != nil {
			return Suppression{}, fmt.Errorf("%s[%d]: %w", label, i, err)
		}
		ids[id] = true
	}
	if len(ids) > 0 {
		result.IDs = ids
	}
	return result, nil
}

func (s Suppression) Clone() Suppression {
	clone := Suppression{Set: s.Set, All: s.All}
	if len(s.IDs) > 0 {
		clone.IDs = make(map[ID]bool, len(s.IDs))
		for id, suppressed := range s.IDs {
			clone.IDs[id] = suppressed
		}
	}
	return clone
}

// Merge unions src into s: when src is set, s becomes set, s.All is OR'd with
// src.All, and src's suppressed IDs are added to s. src's IDs are copied, so later
// mutations of src do not affect s.
func (s *Suppression) Merge(src Suppression) {
	if !src.Set {
		return
	}
	s.Set = true
	if src.All {
		s.All = true
	}
	for id, suppressed := range src.IDs {
		if !suppressed {
			continue
		}
		if s.IDs == nil {
			s.IDs = make(map[ID]bool, len(src.IDs))
		}
		s.IDs[id] = true
	}
}

func (s Suppression) Suppresses(id ID) bool {
	return s.All || s.IDs[id]
}
