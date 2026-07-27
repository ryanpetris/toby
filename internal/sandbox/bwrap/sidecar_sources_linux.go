//go:build linux

package bwrap

// Validates the exact descriptor set used to render agent-owned MCP
// sidecars.

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// SidecarSources contains caller-owned capabilities for every host object in a
// SidecarPlan.
type SidecarSources struct {
	RootFS       *os.File
	OverlayUpper *os.File
	OverlayWork  *os.File
	Binds        map[string]*os.File
	Runtime      *os.File
}

func validateSidecarSources(
	plan SidecarPlan,
	sources SidecarSources,
) error {
	expected := 3 + len(plan.Binds)
	if plan.Runtime != nil {
		expected++
	}
	provided := 3 + len(sources.Binds)
	if sources.Runtime != nil {
		provided++
	}
	if expected > maxSidecarSourceDescriptors ||
		provided > maxSidecarSourceDescriptors {
		return fmt.Errorf(
			"sidecar descriptor count exceeds %d",
			maxSidecarSourceDescriptors,
		)
	}
	if expected != provided {
		return fmt.Errorf(
			"sidecar descriptor count is %d, want %d",
			provided,
			expected,
		)
	}

	for _, source := range []struct {
		label string
		file  *os.File
	}{
		{label: "sidecar rootfs", file: sources.RootFS},
		{label: "sidecar overlay upper", file: sources.OverlayUpper},
		{label: "sidecar overlay work", file: sources.OverlayWork},
	} {
		if err := validateDirectoryDescriptor(source.label, source.file); err != nil {
			return err
		}
	}
	if err := validateDistinctSidecarDirectories(sources); err != nil {
		return err
	}

	expectedTargets := make(map[string]struct{}, len(plan.Binds))
	for _, bind := range plan.Binds {
		expectedTargets[bind.Target] = struct{}{}
		source, found := sources.Binds[bind.Target]
		if !found {
			return fmt.Errorf(
				"missing sidecar bind source %q",
				bind.Target,
			)
		}
		if err := validateBindDescriptor(
			"sidecar bind "+bind.Target,
			source,
			bind.Access,
		); err != nil {
			return err
		}
	}
	for target := range sources.Binds {
		if _, found := expectedTargets[target]; !found {
			return fmt.Errorf("unexpected sidecar bind source %q", target)
		}
	}

	switch {
	case plan.Runtime == nil && sources.Runtime != nil:
		return fmt.Errorf("unexpected sidecar runtime source")
	case plan.Runtime != nil && sources.Runtime == nil:
		return fmt.Errorf("missing sidecar runtime source")
	case plan.Runtime != nil:
		return validateDirectoryDescriptor(
			"sidecar runtime",
			sources.Runtime,
		)
	default:
		return nil
	}
}

func validateDistinctSidecarDirectories(sources SidecarSources) error {
	items := []namedDescriptor{
		{label: "sidecar rootfs", file: sources.RootFS},
		{label: "sidecar overlay upper", file: sources.OverlayUpper},
		{label: "sidecar overlay work", file: sources.OverlayWork},
	}
	identities := make([]descriptorIdentity, len(items))
	for index, item := range items {
		info, err := item.file.Stat()
		if err != nil {
			return fmt.Errorf("inspect %s identity: %w", item.label, err)
		}
		identities[index] = descriptorIdentity{
			label: item.label,
			info:  info,
		}
		for earlier := range index {
			if os.SameFile(info, identities[earlier].info) {
				return fmt.Errorf(
					"%s and %s alias the same directory",
					item.label,
					identities[earlier].label,
				)
			}
		}
	}

	var upper unix.Stat_t
	if err := unix.Fstat(
		int(sources.OverlayUpper.Fd()),
		&upper,
	); err != nil {
		return fmt.Errorf("inspect sidecar overlay upper filesystem: %w", err)
	}
	var work unix.Stat_t
	if err := unix.Fstat(int(sources.OverlayWork.Fd()), &work); err != nil {
		return fmt.Errorf("inspect sidecar overlay work filesystem: %w", err)
	}
	if upper.Dev != work.Dev {
		return fmt.Errorf(
			"sidecar overlay upper and work must share a filesystem",
		)
	}

	return nil
}
