package bwrap

// Validates that one run's writable overlay is a unique sibling pair disjoint
// from every immutable, persistent, external, or executable host path.

import (
	"fmt"
	"path/filepath"
	"strings"
)

type hostPathClaim struct {
	label              string
	path               string
	allowInsideRuntime bool
}

func validateHostStorageGraph(plan Plan) error {
	upperParent := filepath.Dir(plan.Overlay.Upper)
	workParent := filepath.Dir(plan.Overlay.Work)
	if upperParent != workParent {
		return fmt.Errorf(
			"overlay upper and work must be siblings: %q and %q",
			plan.Overlay.Upper,
			plan.Overlay.Work,
		)
	}
	if filepath.Base(plan.Overlay.Upper) != "upper" ||
		filepath.Base(plan.Overlay.Work) != "work" {
		return fmt.Errorf("overlay sibling names must be upper and work")
	}
	expectedRunRoot := filepath.Join(plan.Overlay.RunStorageDir, plan.RunID)
	if upperParent != expectedRunRoot {
		return fmt.Errorf(
			"overlay run root %q must be %q beneath configured run storage",
			upperParent,
			expectedRunRoot,
		)
	}

	runtimeRoot := filepath.Join(expectedRunRoot, "runtime")
	for _, claim := range persistentHostPaths(plan) {
		if claim.allowInsideRuntime &&
			hostPathStrictlyContains(runtimeRoot, claim.path) {
			continue
		}
		if hostPathsOverlap(upperParent, claim.path) {
			return fmt.Errorf(
				"overlay run root %q overlaps %s host path %q",
				upperParent,
				claim.label,
				claim.path,
			)
		}
	}
	if err := validateImmutableRootFS(plan); err != nil {
		return err
	}
	if err := validateOwnedBackings(plan); err != nil {
		return err
	}
	if err := validateGeneratedFileOwnership(plan); err != nil {
		return err
	}

	return nil
}

func validateImmutableRootFS(plan Plan) error {
	for _, claim := range persistentHostPaths(plan) {
		if claim.label == "rootfs" {
			continue
		}
		if hostPathsOverlap(plan.RootFS.Path, claim.path) {
			return fmt.Errorf(
				"rootfs host path %q overlaps %s host path %q",
				plan.RootFS.Path,
				claim.label,
				claim.path,
			)
		}
	}

	return nil
}

func validateOwnedBackings(plan Plan) error {
	owned := make([]hostPathClaim, 0, len(plan.ManagedDirectories)+1)
	owned = append(owned, hostPathClaim{
		label: "private-home",
		path:  plan.Home.HostPath,
	})
	for _, entry := range plan.ManagedDirectories {
		owned = append(owned, hostPathClaim{
			label: "managed-directory " + entry.Key.String(),
			path:  entry.HostPath,
		})
	}

	for index, claim := range owned {
		for earlier := range index {
			other := owned[earlier]
			if hostPathsOverlap(claim.path, other.path) {
				return fmt.Errorf(
					"%s host path %q overlaps %s host path %q",
					claim.label,
					claim.path,
					other.label,
					other.path,
				)
			}
		}
		for _, external := range externalHostPaths(plan) {
			if hostPathsOverlap(claim.path, external.path) {
				return fmt.Errorf(
					"%s host path %q overlaps %s host path %q",
					claim.label,
					claim.path,
					external.label,
					external.path,
				)
			}
		}
	}

	return nil
}

func validateGeneratedFileOwnership(plan Plan) error {
	backings := make([]string, 0, len(plan.ManagedDirectories)+1)
	backings = append(backings, plan.Home.HostPath)
	for _, entry := range plan.ManagedDirectories {
		backings = append(backings, entry.HostPath)
	}

	for index, file := range plan.GeneratedFiles {
		matches := 0
		for _, backing := range backings {
			if hostPathStrictlyContains(backing, file.HostPath) {
				matches++
			}
		}
		if matches != 1 {
			return fmt.Errorf(
				"generated file %d host path %q belongs to %d native backings, want exactly one",
				index,
				file.HostPath,
				matches,
			)
		}
	}

	return nil
}

func externalHostPaths(plan Plan) []hostPathClaim {
	claims := make([]hostPathClaim, 0,
		len(plan.Projects)+len(plan.Binds)+len(plan.RuntimeAssets)+1,
	)
	claims = append(claims, hostPathClaim{
		label: "sandbox helper",
		path:  plan.SandboxBinary.HostPath,
	})
	for _, project := range plan.Projects {
		claims = append(claims, hostPathClaim{
			label: "project " + project.Name,
			path:  project.HostPath,
		})
	}
	for index, bind := range plan.Binds {
		claims = append(claims, hostPathClaim{
			label: fmt.Sprintf("bind %d", index),
			path:  bind.HostPath,
		})
	}
	for index, asset := range plan.RuntimeAssets {
		claims = append(claims, hostPathClaim{
			label:              fmt.Sprintf("runtime asset %d", index),
			path:               asset.HostPath,
			allowInsideRuntime: true,
		})
	}

	return claims
}

func persistentHostPaths(plan Plan) []hostPathClaim {
	claims := make([]hostPathClaim, 0,
		3+
			len(plan.Projects)+
			len(plan.ManagedDirectories)+
			len(plan.Binds)+
			len(plan.RuntimeAssets)+
			len(plan.GeneratedFiles),
	)
	claims = append(claims,
		hostPathClaim{label: "rootfs", path: plan.RootFS.Path},
		hostPathClaim{label: "private-home", path: plan.Home.HostPath},
		hostPathClaim{label: "sandbox helper", path: plan.SandboxBinary.HostPath},
	)
	for _, project := range plan.Projects {
		claims = append(claims, hostPathClaim{
			label: "project " + project.Name,
			path:  project.HostPath,
		})
	}
	for _, entry := range plan.ManagedDirectories {
		claims = append(claims, hostPathClaim{
			label: "managed-directory " + entry.Key.String(),
			path:  entry.HostPath,
		})
	}
	for index, bind := range plan.Binds {
		claims = append(claims, hostPathClaim{
			label: fmt.Sprintf("bind %d", index),
			path:  bind.HostPath,
		})
	}
	for index, asset := range plan.RuntimeAssets {
		claims = append(claims, hostPathClaim{
			label:              fmt.Sprintf("runtime asset %d", index),
			path:               asset.HostPath,
			allowInsideRuntime: true,
		})
	}
	for index, file := range plan.GeneratedFiles {
		claims = append(claims, hostPathClaim{
			label: fmt.Sprintf("generated file %d", index),
			path:  file.HostPath,
		})
	}

	return claims
}

func validateHostPath(label string, value string) error {
	if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value ||
		strings.ContainsRune(value, 0) {
		return fmt.Errorf("%s host path must be clean and absolute: %q", label, value)
	}

	return nil
}

func hostPathsOverlap(first string, second string) bool {
	firstToSecond, err := filepath.Rel(first, second)
	if err == nil && pathIsWithin(firstToSecond) {
		return true
	}
	secondToFirst, err := filepath.Rel(second, first)

	return err == nil && pathIsWithin(secondToFirst)
}

func hostPathStrictlyContains(parent string, child string) bool {
	relative, err := filepath.Rel(parent, child)

	return err == nil && relative != "." && pathIsWithin(relative)
}

func pathIsWithin(relative string) bool {
	return relative == "." ||
		(relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}
