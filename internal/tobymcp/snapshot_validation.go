package tobymcp

// Validates native session snapshot bounds, identifiers, paths, and aggregate
// uniqueness without accepting any extensible secret-bearing maps.

import (
	"fmt"
	"regexp"

	"petris.dev/toby/internal/sandbox/layout"
)

const (
	// MaxSnapshotCollectionItems bounds each repeated session snapshot field.
	MaxSnapshotCollectionItems = 256

	maxSessionSnapshotItems = MaxSnapshotCollectionItems
	maxSessionTextBytes     = 256
	maxSessionPathBytes     = 4 << 10
)

var sessionRootFSDigestPattern = regexp.MustCompile(
	`^sha256:[0-9a-f]{64}$`,
)

// Validate checks that a session snapshot is a bounded native Bubblewrap view.
func (s SessionSnapshot) Validate() error {
	if err := s.Runtime.validate(); err != nil {
		return err
	}
	if err := s.Tools.validate(); err != nil {
		return err
	}
	if err := validateSessionProjects(s.Projects); err != nil {
		return err
	}
	if err := validateSessionMounts(s.Mounts); err != nil {
		return err
	}
	if err := validateSessionBinds(s.Binds); err != nil {
		return err
	}
	if err := validateSessionModels(s.Models); err != nil {
		return err
	}

	return validateSessionMCPs(s.MCPs)
}

func (s SessionRuntime) validate() error {
	if err := validateSessionText("runtime environment name", s.Name); err != nil {
		return err
	}
	if err := validateSessionText("runtime profile", s.Profile); err != nil {
		return err
	}
	if s.Runtime != "bubblewrap" {
		return fmt.Errorf(
			"session runtime must be bubblewrap, got %q",
			s.Runtime,
		)
	}
	if s.RootFSDigest != "" &&
		!sessionRootFSDigestPattern.MatchString(s.RootFSDigest) {
		return fmt.Errorf("session rootfs digest is invalid")
	}
	if s.Network != "" && s.Network != "host" && s.Network != "private" {
		return fmt.Errorf(
			"session runtime network is invalid: %q",
			s.Network,
		)
	}

	for _, field := range []struct {
		name     string
		value    string
		expected string
	}{
		{name: "home", value: s.Home, expected: layout.Home},
		{name: "workspace", value: s.Workspace, expected: layout.Workspace},
		{name: "root", value: s.Root, expected: layout.Root},
		{name: "bin", value: s.Bin, expected: layout.Bin},
	} {
		if field.value != field.expected {
			return fmt.Errorf(
				"session runtime %s must be %s",
				field.name,
				field.expected,
			)
		}
	}
	if err := validateSessionPath(
		"session runtime workdir",
		s.Workdir,
	); err != nil {
		return err
	}

	return nil
}

func (s SessionTools) validate() error {
	if s.Primary != "" {
		if err := validateSessionText("primary tool", s.Primary); err != nil {
			return err
		}
	}
	if len(s.Active) > maxSessionSnapshotItems {
		return fmt.Errorf(
			"active tool count exceeds %d",
			maxSessionSnapshotItems,
		)
	}
	if err := validateUniqueSessionStrings("active tool", s.Active); err != nil {
		return err
	}
	if len(s.Available) > maxSessionSnapshotItems {
		return fmt.Errorf(
			"available tool count exceeds %d",
			maxSessionSnapshotItems,
		)
	}

	available := make(map[string]struct{}, len(s.Available))
	for index, tool := range s.Available {
		if err := validateSessionText(
			fmt.Sprintf("available tool %d name", index),
			tool.Name,
		); err != nil {
			return err
		}
		if _, duplicate := available[tool.Name]; duplicate {
			return fmt.Errorf(
				"available tool %d duplicates %q",
				index,
				tool.Name,
			)
		}
		available[tool.Name] = struct{}{}
		if err := validateUniqueSessionStrings(
			fmt.Sprintf(
				"available tool %q context group",
				tool.Name,
			),
			tool.ContextGroups,
		); err != nil {
			return err
		}
	}

	for name, group := range s.Groups {
		if err := validateSessionText("tool group name", name); err != nil {
			return err
		}
		if err := validateSessionText(
			fmt.Sprintf("tool %q group", name),
			group,
		); err != nil {
			return err
		}
		if _, exists := available[name]; !exists {
			return fmt.Errorf(
				"tool group references unavailable tool %q",
				name,
			)
		}
	}

	if s.Primary != "" {
		if _, exists := available[s.Primary]; !exists {
			return fmt.Errorf(
				"primary tool %q is unavailable",
				s.Primary,
			)
		}
	}
	for _, name := range s.Active {
		if _, exists := available[name]; !exists {
			return fmt.Errorf("active tool %q is unavailable", name)
		}
	}

	return nil
}
