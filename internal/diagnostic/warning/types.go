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
	// PermissionPathInvalid warns that a permission path mode is not allow or deny.
	PermissionPathInvalid ID = "permission.path-invalid"
	// AgentBinaryMismatch warns that the client and agent builds differ.
	AgentBinaryMismatch ID = "agent.binary-version-mismatch"
	// ConfigUnknownWarningID warns that settings.suppressWarnings named an unknown ID.
	ConfigUnknownWarningID ID = "config.unknown-warning-id"
	// ConfigInstructionMissing warns that an instruction path or glob matched nothing.
	ConfigInstructionMissing ID = "config.instruction-missing"
	// ConfigFragmentIgnored warns that a config.d file was not loaded.
	ConfigFragmentIgnored ID = "config.fragment-ignored"
	// MCPServerInvalid warns that one MCP server definition was skipped.
	MCPServerInvalid ID = "mcp.server-invalid"
	// MCPImageUnavailable warns that a local MCP sidecar image could not be prepared.
	MCPImageUnavailable ID = "mcp.image-unavailable"
	// ModelsEndpointUnavailable warns that one models endpoint was skipped.
	ModelsEndpointUnavailable ID = "models.endpoint-unavailable"
	// DockerSocketMissing warns that the Docker engine socket is absent.
	DockerSocketMissing ID = "docker.socket-missing"
)

var registeredIDs = []ID{
	AgentBinaryMismatch,
	ConfigFragmentIgnored,
	ConfigInstructionMissing,
	ConfigUnknownWarningID,
	DockerSocketMissing,
	MCPImageUnavailable,
	MCPServerInvalid,
	ModelsEndpointUnavailable,
	PermissionAutoDeny,
	PermissionPathInvalid,
	ProjectAutoloadDisabled,
	ProjectDuplicate,
	ProjectMissing,
}

// ParseID parses and validates a warning identifier.
func ParseID(value string) (ID, error) {
	id := ID(strings.TrimSpace(value))
	for _, registered := range registeredIDs {
		if id == registered {
			return id, nil
		}
	}
	return "", fmt.Errorf("warning id %q is not registered", strings.TrimSpace(value))
}

// Suppression records which warnings a user has disabled.
type Suppression struct {
	Set bool
	All bool
	IDs map[ID]bool
}

// SuppressionFromList builds a Suppression from the list form of
// settings.suppressWarnings. The single entry "*" suppresses every warning.
// Unknown IDs are omitted from the suppression set and returned so the caller
// can warn; they do not fail configuration loading.
func SuppressionFromList(list []string) (Suppression, []string) {
	result := Suppression{Set: true}
	ids := map[ID]bool{}
	var unknown []string
	for _, item := range list {
		if strings.TrimSpace(item) == "*" {
			result.All = true
			continue
		}
		id, err := ParseID(item)
		if err != nil {
			unknown = append(unknown, item)
			continue
		}
		ids[id] = true
	}
	if len(ids) > 0 {
		result.IDs = ids
	}
	return result, unknown
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

// WarnUnknownSuppressWarnings emits one warning per unknown suppressWarnings ID.
func WarnUnknownSuppressWarnings(warnings *Service, ids []string) {
	if warnings == nil {
		return
	}
	for _, id := range ids {
		warnings.Warn(
			ConfigUnknownWarningID,
			fmt.Sprintf(
				"settings.suppressWarnings includes unknown id %q; ignoring it",
				id,
			),
			"configured_id", id,
		)
	}
}
