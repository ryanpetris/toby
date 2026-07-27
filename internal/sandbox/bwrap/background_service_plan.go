package bwrap

// Defines the immutable read-only sandbox plan for an agent-owned background
// service.

import (
	"fmt"
	"sort"
	"strings"

	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/sandbox/mount"
)

const maxBackgroundServiceSourceDescriptors = 4096

const (
	// BackgroundServiceRuntimeTarget is the only writable host directory a
	// background-service plan may expose.
	BackgroundServiceRuntimeTarget = layout.Runtime + "/service"

	// BackgroundServiceAuthSocketTarget is the fixed protected authorization
	// socket target available to a background service.
	BackgroundServiceAuthSocketTarget = layout.Runtime + "/auth.sock"
)

// BackgroundServicePlan is the complete immutable description of one
// agent-owned noninteractive service whose root filesystem is read-only when
// its payload starts.
type BackgroundServicePlan struct {
	ID     string
	RootFS RootFS

	// Binds contains exact caller-authorized capabilities. Callers remain
	// responsible for applying the service-specific target allowlist.
	Binds []mount.Bind

	Runtime     *RuntimeAsset
	Workdir     string
	Environment []EnvironmentVariable
	Identity    Identity
	Network     NetworkMode

	// Command is the fixed, non-secret payload command. Bubblewrap exposes it
	// in the outer process argument vector because version 0.11.2 cannot parse
	// a payload command from a nested --args source.
	Command []string
}

// Canonical returns a detached plan with ordering-insensitive fields sorted.
func (p BackgroundServicePlan) Canonical() BackgroundServicePlan {
	clone := p.Clone()
	sort.Slice(clone.Binds, func(i, j int) bool {
		first := clone.Binds[i]
		second := clone.Binds[j]
		if first.Target != second.Target {
			return first.Target < second.Target
		}
		if first.HostPath != second.HostPath {
			return first.HostPath < second.HostPath
		}
		if first.Access != second.Access {
			return first.Access < second.Access
		}
		return !first.Optional && second.Optional
	})
	sort.Slice(clone.Environment, func(i, j int) bool {
		if clone.Environment[i].Name == clone.Environment[j].Name {
			return clone.Environment[i].Value < clone.Environment[j].Value
		}
		return clone.Environment[i].Name < clone.Environment[j].Name
	})

	return clone
}

// Clone returns an isolated plan copy.
func (p BackgroundServicePlan) Clone() BackgroundServicePlan {
	clone := p
	clone.Binds = append([]mount.Bind(nil), p.Binds...)
	clone.Environment = append(
		[]EnvironmentVariable(nil),
		p.Environment...,
	)
	clone.Command = append([]string(nil), p.Command...)
	if p.Runtime != nil {
		runtime := *p.Runtime
		clone.Runtime = &runtime
	}

	return clone
}

// Validate checks the complete background-service host and sandbox path
// graphs.
func (p BackgroundServicePlan) Validate() error {
	descriptorCount := 1 + len(p.Binds)
	if p.Runtime != nil {
		descriptorCount++
	}
	if descriptorCount > maxBackgroundServiceSourceDescriptors {
		return fmt.Errorf(
			"background-service descriptor count exceeds %d",
			maxBackgroundServiceSourceDescriptors,
		)
	}
	if !idPattern.MatchString(p.ID) {
		return fmt.Errorf("invalid background-service id %q", p.ID)
	}
	if !digestPattern.MatchString(p.RootFS.Digest) {
		return fmt.Errorf(
			"invalid background-service rootfs digest %q",
			p.RootFS.Digest,
		)
	}
	if err := validateHostPath(
		"background-service rootfs",
		p.RootFS.Path,
	); err != nil {
		return err
	}
	if err := validateSandboxPath(
		"background-service workdir",
		p.Workdir,
	); err != nil {
		return err
	}
	if p.Identity.HostUID < 0 || p.Identity.HostGID < 0 {
		return fmt.Errorf(
			"background-service host uid and gid must be non-negative",
		)
	}
	switch p.Network {
	case NetworkHost, NetworkPrivate:
	default:
		return fmt.Errorf(
			"invalid background-service network mode %q",
			p.Network,
		)
	}
	if len(p.Command) == 0 || p.Command[0] == "" {
		return fmt.Errorf(
			"background-service command argv must not be empty",
		)
	}
	for _, argument := range p.Command {
		if strings.ContainsRune(argument, 0) {
			return fmt.Errorf(
				"background-service command argv contains a NUL byte",
			)
		}
	}
	if err := validateEnvironment(p.Environment); err != nil {
		return err
	}
	if err := validateBackgroundServiceBinds(p.Binds); err != nil {
		return err
	}
	if err := validateBackgroundServiceRuntime(p.Runtime); err != nil {
		return err
	}
	if p.Runtime == nil &&
		(p.Workdir == BackgroundServiceRuntimeTarget ||
			strings.HasPrefix(
				p.Workdir,
				BackgroundServiceRuntimeTarget+"/",
			)) {
		return fmt.Errorf(
			"background-service workdir %q requires the writable runtime",
			p.Workdir,
		)
	}

	return validateBackgroundServiceHostGraph(p)
}

func validateBackgroundServiceBinds(binds []mount.Bind) error {
	targets := make([]string, 0, len(binds))
	for index, bind := range binds {
		if err := bind.Validate(); err != nil {
			return fmt.Errorf(
				"background-service bind %d: %w",
				index,
				err,
			)
		}
		if err := validateHostPath(
			"background-service bind",
			bind.HostPath,
		); err != nil {
			return fmt.Errorf(
				"background-service bind %d: %w",
				index,
				err,
			)
		}
		if bind.Optional {
			return fmt.Errorf(
				"background-service bind %d must not be optional",
				index,
			)
		}

		switch bind.Target {
		case BackgroundServiceAuthSocketTarget:
			if bind.Access != mount.AccessReadOnly {
				return fmt.Errorf(
					"background-service authorization socket must be read-only",
				)
			}
		default:
			if bind.Access != mount.AccessReadOnly {
				return fmt.Errorf(
					"background-service bind target %q must be read-only",
					bind.Target,
				)
			}
			for _, reserved := range []string{
				"/proc",
				"/dev",
				"/tmp",
				"/run",
			} {
				if mount.TargetsOverlap(bind.Target, reserved) {
					return fmt.Errorf(
						"background-service bind target %q overlaps reserved path %q",
						bind.Target,
						reserved,
					)
				}
			}
		}

		for _, target := range targets {
			if mount.TargetsOverlap(bind.Target, target) {
				return fmt.Errorf(
					"overlapping background-service bind targets %q and %q",
					target,
					bind.Target,
				)
			}
		}
		targets = append(targets, bind.Target)
	}

	return nil
}

func validateBackgroundServiceRuntime(runtime *RuntimeAsset) error {
	if runtime == nil {
		return nil
	}
	if runtime.Target != BackgroundServiceRuntimeTarget {
		return fmt.Errorf(
			"background-service runtime target must be %s",
			BackgroundServiceRuntimeTarget,
		)
	}
	if runtime.Access != mount.AccessRegular {
		return fmt.Errorf(
			"background-service runtime directory must be writable",
		)
	}
	if err := validateHostPath(
		"background-service runtime directory",
		runtime.HostPath,
	); err != nil {
		return err
	}

	return nil
}

func validateBackgroundServiceHostGraph(
	plan BackgroundServicePlan,
) error {
	claims := make([]hostPathClaim, 0, len(plan.Binds)+2)
	claims = append(claims, hostPathClaim{
		label: "background-service rootfs",
		path:  plan.RootFS.Path,
	})
	for index, bind := range plan.Binds {
		claims = append(claims, hostPathClaim{
			label: fmt.Sprintf("background-service bind %d", index),
			path:  bind.HostPath,
		})
	}
	if plan.Runtime != nil {
		claims = append(claims, hostPathClaim{
			label: "background-service runtime directory",
			path:  plan.Runtime.HostPath,
		})
	}

	for index, claim := range claims {
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
