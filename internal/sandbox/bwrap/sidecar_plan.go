package bwrap

// Defines and validates the smaller Bubblewrap plan used by agent-owned MCP
// sidecars, which have no private home, projects, generated files, or Toby
// binary.

import (
	"fmt"
	"path/filepath"
	"sort"

	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/sandbox/mount"
)

const maxSidecarSourceDescriptors = 4096

// SidecarPlan is the complete immutable description of one agent-owned
// noninteractive process.
type SidecarPlan struct {
	ID          string
	RootFS      RootFS
	Overlay     Overlay
	Binds       []mount.Bind
	Runtime     *RuntimeAsset
	Workdir     string
	Environment []EnvironmentVariable
	Identity    Identity
	Network     NetworkMode
	Command     []string
}

// Canonical returns a detached plan with ordering-insensitive fields sorted.
func (p SidecarPlan) Canonical() SidecarPlan {
	clone := p.Clone()
	sort.Slice(clone.Binds, func(i, j int) bool {
		if clone.Binds[i].Target == clone.Binds[j].Target {
			return clone.Binds[i].HostPath < clone.Binds[j].HostPath
		}
		return clone.Binds[i].Target < clone.Binds[j].Target
	})
	sort.Slice(clone.Environment, func(i, j int) bool {
		return clone.Environment[i].Name < clone.Environment[j].Name
	})

	return clone
}

// Clone returns an isolated plan copy.
func (p SidecarPlan) Clone() SidecarPlan {
	clone := p
	clone.Binds = append([]mount.Bind(nil), p.Binds...)
	clone.Environment = append([]EnvironmentVariable(nil), p.Environment...)
	clone.Command = append([]string(nil), p.Command...)
	if p.Runtime != nil {
		runtime := *p.Runtime
		clone.Runtime = &runtime
	}

	return clone
}

// Validate checks the complete sidecar host and sandbox path graphs.
func (p SidecarPlan) Validate() error {
	if !generatedRunIDPattern.MatchString(p.ID) {
		return fmt.Errorf("invalid sidecar id %q", p.ID)
	}
	if !digestPattern.MatchString(p.RootFS.Digest) {
		return fmt.Errorf("invalid sidecar rootfs digest %q", p.RootFS.Digest)
	}
	for _, hostPath := range []struct {
		label string
		path  string
	}{
		{label: "rootfs", path: p.RootFS.Path},
		{label: "run storage", path: p.Overlay.RunStorageDir},
		{label: "overlay upper", path: p.Overlay.Upper},
		{label: "overlay work", path: p.Overlay.Work},
	} {
		if err := validateHostPath(hostPath.label, hostPath.path); err != nil {
			return err
		}
	}
	if err := validateSidecarOverlay(p); err != nil {
		return err
	}
	if err := validateSandboxPath("sidecar workdir", p.Workdir); err != nil {
		return err
	}
	if p.Identity.HostUID < 0 || p.Identity.HostGID < 0 {
		return fmt.Errorf("sidecar host uid and gid must be non-negative")
	}
	switch p.Network {
	case NetworkHost, NetworkPrivate:
	default:
		return fmt.Errorf("invalid sidecar network mode %q", p.Network)
	}
	if len(p.Command) == 0 || p.Command[0] == "" {
		return fmt.Errorf("sidecar command argv must not be empty")
	}
	for _, argument := range p.Command {
		if containsNUL(argument) {
			return fmt.Errorf("sidecar command argv contains a NUL byte")
		}
	}
	if err := validateEnvironment(p.Environment); err != nil {
		return err
	}
	if err := validateSidecarBinds(p.Binds, p.Runtime); err != nil {
		return err
	}

	return validateSidecarHostGraph(p)
}

func validateSidecarOverlay(plan SidecarPlan) error {
	if filepath.Clean(plan.Overlay.Upper) ==
		filepath.Clean(plan.Overlay.Work) {
		return fmt.Errorf("sidecar overlay upper and work must differ")
	}
	upperParent := filepath.Dir(plan.Overlay.Upper)
	workParent := filepath.Dir(plan.Overlay.Work)
	if upperParent != workParent ||
		filepath.Base(plan.Overlay.Upper) != "upper" ||
		filepath.Base(plan.Overlay.Work) != "work" {
		return fmt.Errorf(
			"sidecar overlay upper and work must be named sibling directories",
		)
	}
	expected := filepath.Join(plan.Overlay.RunStorageDir, plan.ID)
	if upperParent != expected {
		return fmt.Errorf(
			"sidecar overlay root %q must be %q",
			upperParent,
			expected,
		)
	}

	return nil
}

func validateSidecarBinds(
	binds []mount.Bind,
	runtime *RuntimeAsset,
) error {
	targets := make([]string, 0, len(binds)+1)
	for index, bind := range binds {
		if err := bind.Validate(); err != nil {
			return fmt.Errorf("sidecar bind %d: %w", index, err)
		}
		if err := validateHostPath("sidecar bind", bind.HostPath); err != nil {
			return fmt.Errorf("sidecar bind %d: %w", index, err)
		}
		for _, reserved := range []string{"/proc", "/dev", "/tmp", "/run"} {
			if mount.TargetsOverlap(bind.Target, reserved) {
				return fmt.Errorf(
					"sidecar bind target %q overlaps reserved path %q",
					bind.Target,
					reserved,
				)
			}
		}
		for _, target := range targets {
			if mount.TargetsOverlap(bind.Target, target) {
				return fmt.Errorf(
					"overlapping sidecar bind targets %q and %q",
					target,
					bind.Target,
				)
			}
		}
		targets = append(targets, bind.Target)
	}

	if runtime == nil {
		return nil
	}
	if runtime.Target != layout.Runtime {
		return fmt.Errorf(
			"sidecar runtime target must be %s",
			layout.Runtime,
		)
	}
	if runtime.Access != mount.AccessRegular {
		return fmt.Errorf("sidecar runtime directory must be writable")
	}
	if err := validateHostPath(
		"sidecar runtime directory",
		runtime.HostPath,
	); err != nil {
		return err
	}

	return nil
}

func validateSidecarHostGraph(plan SidecarPlan) error {
	runRoot := filepath.Dir(plan.Overlay.Upper)
	claims := []hostPathClaim{{
		label: "sidecar rootfs",
		path:  plan.RootFS.Path,
	}}
	for index, bind := range plan.Binds {
		claims = append(claims, hostPathClaim{
			label: fmt.Sprintf("sidecar bind %d", index),
			path:  bind.HostPath,
		})
	}
	if plan.Runtime != nil {
		expectedRuntime := filepath.Join(runRoot, "runtime")
		if plan.Runtime.HostPath != expectedRuntime {
			return fmt.Errorf(
				"sidecar runtime directory %q must be %q",
				plan.Runtime.HostPath,
				expectedRuntime,
			)
		}
		claims = append(claims, hostPathClaim{
			label: "sidecar runtime directory",
			path:  plan.Runtime.HostPath,
		})
	}

	for index, claim := range claims {
		if claim.label != "sidecar runtime directory" &&
			hostPathsOverlap(runRoot, claim.path) {
			return fmt.Errorf(
				"sidecar overlay root %q overlaps %s host path %q",
				runRoot,
				claim.label,
				claim.path,
			)
		}
		for earlier := range index {
			other := claims[earlier]
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
	}

	return nil
}

func containsNUL(value string) bool {
	for _, character := range value {
		if character == 0 {
			return true
		}
	}
	return false
}
