package resource

// Implements the user-to-run scope lattice used to prevent unsafe resource
// sharing when mutable mounts or run authority are present.

import "fmt"

// Scope identifies the identity boundary within which a resource may be reused.
type Scope string

const (
	// ScopeUser permits reuse across all runs for a user.
	ScopeUser Scope = "user"
	// ScopeHome limits reuse to one persistent home.
	ScopeHome Scope = "home"
	// ScopeProject limits reuse to one project.
	ScopeProject Scope = "project"
	// ScopeRun limits reuse to one launch.
	ScopeRun Scope = "run"
)

// RunAuthority declares whether a resource receives generated authority whose
// lifetime is limited to one run. Its zero value is invalid so callers cannot
// accidentally omit the declaration and receive a reusable resource.
type RunAuthority string

const (
	// RunAuthorityAbsent means the resource has no run-specific authority.
	RunAuthorityAbsent RunAuthority = "absent"
	// RunAuthorityPresent means the resource has run-specific authority.
	RunAuthorityPresent RunAuthority = "present"
)

func (s Scope) rank() (int, error) {
	switch s {
	case ScopeUser:
		return 0, nil
	case ScopeHome:
		return 1, nil
	case ScopeProject:
		return 2, nil
	case ScopeRun:
		return 3, nil
	default:
		return 0, fmt.Errorf("invalid resource scope %q", s)
	}
}

// EffectiveScope narrows requested as required by mounted data and run-only
// authority. Callers must declare whether run authority is present. It never
// returns a scope broader than requested.
func EffectiveScope(requested Scope, mounts []Mount, runAuthority RunAuthority) (Scope, error) {
	rank, err := requested.rank()
	if err != nil {
		return "", err
	}
	effective := requested

	for _, mount := range mounts {
		mountRank, err := mount.Scope.rank()
		if err != nil {
			return "", fmt.Errorf("mount %q: %w", mount.Target, err)
		}
		if mountRank > rank {
			rank = mountRank
			effective = mount.Scope
		}
	}

	switch runAuthority {
	case RunAuthorityAbsent:
	case RunAuthorityPresent:
		effective = ScopeRun
	default:
		return "", fmt.Errorf("invalid run authority declaration %q", runAuthority)
	}

	return effective, nil
}
