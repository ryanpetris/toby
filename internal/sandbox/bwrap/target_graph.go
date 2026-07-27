package bwrap

// Validates the complete sandbox target namespace and maps every generated
// native file to its private-home or managed-directory backing.

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/sandbox/mount"
)

type persistentBacking struct {
	hostRoot    string
	sandboxRoot string
}

func validateTargetGraph(plan Plan) error {
	mountTargets := make([]string, 0, len(plan.ManagedDirectories)+len(plan.Binds))
	for _, entry := range plan.ManagedDirectories {
		mountTargets = append(mountTargets, entry.Target)
	}
	for _, bind := range plan.Binds {
		mountTargets = append(mountTargets, bind.Target)
	}

	for _, target := range mountTargets {
		if target == layout.Root || sandboxPathContains(target, layout.Root) {
			return fmt.Errorf("sandbox mount target %q shadows Toby's layout root", target)
		}
		if target == layout.Home || sandboxPathContains(target, layout.Home) {
			return fmt.Errorf("sandbox mount target %q shadows the private home", target)
		}
		for _, reserved := range []string{layout.Workspace, layout.Bin, layout.Runtime} {
			if mount.TargetsOverlap(target, reserved) {
				return fmt.Errorf("sandbox mount target %q overlaps reserved path %q", target, reserved)
			}
		}
		for _, project := range plan.Projects {
			if mount.TargetsOverlap(target, project.Target) {
				return fmt.Errorf(
					"sandbox mount target %q overlaps project %q at %q",
					target,
					project.Name,
					project.Target,
				)
			}
		}
	}

	return nil
}

func validateGeneratedFiles(plan Plan) error {
	backings := make([]persistentBacking, 0, len(plan.ManagedDirectories)+1)
	backings = append(backings, persistentBacking{
		hostRoot:    plan.Home.HostPath,
		sandboxRoot: layout.Home,
	})
	for _, entry := range plan.ManagedDirectories {
		backings = append(backings, persistentBacking{
			hostRoot:    entry.HostPath,
			sandboxRoot: entry.Target,
		})
	}

	for index, file := range plan.GeneratedFiles {
		if err := validateGeneratedFile(file, backings, plan.Identity); err != nil {
			return fmt.Errorf("generated file %d: %w", index, err)
		}
		for earlier := range index {
			other := plan.GeneratedFiles[earlier]
			if mount.TargetsOverlap(other.Target, file.Target) {
				return fmt.Errorf(
					"generated-file targets %q and %q overlap",
					other.Target,
					file.Target,
				)
			}
			if filepath.Clean(other.HostPath) == filepath.Clean(file.HostPath) {
				return fmt.Errorf("duplicate generated-file host path %q", file.HostPath)
			}
		}
		for _, bind := range plan.Binds {
			if mount.TargetsOverlap(file.Target, bind.Target) {
				return fmt.Errorf(
					"generated-file target %q overlaps external bind %q",
					file.Target,
					bind.Target,
				)
			}
		}
	}

	return nil
}

func validateGeneratedFile(
	file GeneratedFile,
	backings []persistentBacking,
	identity Identity,
) error {
	if err := validateHostPath("generated-file", file.HostPath); err != nil {
		return err
	}
	if err := validateSandboxPath("generated-file target", file.Target); err != nil {
		return err
	}
	if file.Mode.Perm() == 0 || file.Mode&^fs.ModePerm != 0 {
		return fmt.Errorf("mode must contain only permission bits: %v", file.Mode)
	}
	if file.Mode.Perm()&0o600 == 0 {
		return fmt.Errorf("mode must grant the owner read or write permission: %v", file.Mode)
	}
	if file.UID < 0 || file.GID < 0 {
		return fmt.Errorf("uid and gid must be non-negative")
	}
	if file.UID != identity.HostUID || file.GID != identity.HostGID {
		return fmt.Errorf(
			"uid:gid %d:%d must match the plan host identity %d:%d",
			file.UID,
			file.GID,
			identity.HostUID,
			identity.HostGID,
		)
	}

	backing, relative, found := generatedFileBacking(file.Target, backings)
	if !found {
		return fmt.Errorf(
			"target %q is not beneath the private home or a managed directory",
			file.Target,
		)
	}
	expectedHostPath := filepath.Join(backing.hostRoot, filepath.FromSlash(relative))
	if filepath.Clean(file.HostPath) != filepath.Clean(expectedHostPath) {
		return fmt.Errorf(
			"host path %q does not map target %q through backing %q",
			file.HostPath,
			file.Target,
			backing.sandboxRoot,
		)
	}

	return nil
}

func generatedFileBacking(
	target string,
	backings []persistentBacking,
) (persistentBacking, string, bool) {
	var selected persistentBacking
	for _, backing := range backings {
		if !sandboxPathContains(backing.sandboxRoot, target) {
			continue
		}
		if selected.sandboxRoot == "" ||
			len(backing.sandboxRoot) > len(selected.sandboxRoot) {
			selected = backing
		}
	}
	if selected.sandboxRoot == "" {
		return persistentBacking{}, "", false
	}

	return selected, strings.TrimPrefix(target, selected.sandboxRoot+"/"), true
}

func sandboxPathContains(parent string, child string) bool {
	return strings.HasPrefix(child, parent+"/")
}
