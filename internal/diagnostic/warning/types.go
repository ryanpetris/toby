package warning

// Warning identity and suppression config: the registered warning IDs (with
// parsing/validation) and the Suppression set recording which IDs — or all of
// them — the user has silenced, with merge/clone helpers for layered config.

import (
	"fmt"
	"strings"
)

// ID identifies a suppressible warning.
type ID string

const (
	// ProjectAutoloadDisabled warns that project configuration was not loaded.
	ProjectAutoloadDisabled ID = "project.autoload-disabled"
	// ProjectDuplicate warns that project configuration was supplied twice.
	ProjectDuplicate ID = "project.duplicate"
	// ProjectMissing warns that a configured project path is absent.
	ProjectMissing ID = "project.missing"
	// PermissionAutoDeny warns that a permission request was denied automatically.
	PermissionAutoDeny ID = "permission.auto-deny"
	// AgentBinaryMismatch warns that the client and agent builds differ.
	AgentBinaryMismatch ID = "agent.binary-version-mismatch"
)

// ParseID parses and validates a warning identifier.
func ParseID(value string) (ID, error) {
	switch id := ID(strings.TrimSpace(value)); id {
	case ProjectAutoloadDisabled,
		ProjectDuplicate,
		ProjectMissing,
		PermissionAutoDeny,
		AgentBinaryMismatch:
		return id, nil
	default:
		return "", fmt.Errorf(
			"warning id must be one of %q, %q, %q, %q, or %q",
			ProjectAutoloadDisabled,
			ProjectDuplicate,
			ProjectMissing,
			PermissionAutoDeny,
			AgentBinaryMismatch,
		)
	}
}

// Suppression records which warnings a user has disabled.
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

// Clone returns an independent copy of the suppression set.
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

// Suppresses reports whether id is disabled.
func (s Suppression) Suppresses(id ID) bool {
	return s.All || s.IDs[id]
}
