//go:build linux

package bwrap

// Renders one descriptor-authoritative agent sidecar as a fixed-policy
// noninteractive Bubblewrap invocation.

import (
	"fmt"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/sandbox/layout"
)

// RenderSidecar validates and renders an agent-owned MCP sidecar without
// reopening any host path.
func RenderSidecar(
	plan SidecarPlan,
	sources SidecarSources,
) (result *Invocation, returnErr error) {
	canonical := plan.Canonical()
	if err := canonical.Validate(); err != nil {
		return nil, fmt.Errorf("validate Bubblewrap sidecar plan: %w", err)
	}
	if err := validateSidecarSources(canonical, sources); err != nil {
		return nil, fmt.Errorf(
			"validate Bubblewrap sidecar sources: %w",
			err,
		)
	}

	invocation := &Invocation{Mode: ExecutionNonInteractive}
	defer func() {
		if returnErr != nil {
			diagnostic.DiscardError(
				"sidecar rendering already failed",
				"close partial Bubblewrap sidecar invocation",
				invocation.Close(),
			)
			result = nil
		}
	}()

	rootFD, err := invocation.retain(sources.RootFS, "sidecar rootfs")
	if err != nil {
		return nil, err
	}
	upperFD, err := invocation.retain(
		sources.OverlayUpper,
		"sidecar overlay upper",
	)
	if err != nil {
		return nil, err
	}
	workFD, err := invocation.retain(
		sources.OverlayWork,
		"sidecar overlay work",
	)
	if err != nil {
		return nil, err
	}

	args := namespaceArgs(
		canonical.Identity.HostUID,
		canonical.Identity.HostGID,
	)
	if canonical.Network == NetworkPrivate {
		args = append(args, "--unshare-net")
	}
	args = append(args,
		"--cap-drop", "ALL",
		"--overlay-src", childFDPath(rootFD),
		"--overlay",
		childFDPath(upperFD),
		childFDPath(workFD),
		"/",
	)
	args = appendOverlayFDRegistrations(args, rootFD, upperFD, workFD)
	args = append(args,
		"--proc", "/proc",
		"--dev", "/dev",
		"--tmpfs", "/tmp",
		"--dir", "/run",
		"--dir", layout.Runtime,
		"--chmod", "0700", layout.Runtime,
	)

	for _, bind := range canonical.Binds {
		fd, err := invocation.retain(
			sources.Binds[bind.Target],
			"sidecar bind "+bind.Target,
		)
		if err != nil {
			return nil, err
		}
		args = appendFDBind(args, bind.Access, fd, bind.Target)
	}
	if canonical.Runtime != nil {
		fd, err := invocation.retain(
			sources.Runtime,
			"sidecar runtime directory",
		)
		if err != nil {
			return nil, err
		}
		args = appendFDBind(
			args,
			canonical.Runtime.Access,
			fd,
			canonical.Runtime.Target,
		)
	}

	args = append(args,
		"--clearenv",
		"--setenv", "HOME", "/tmp",
		"--setenv", "TOBY_SANDBOX", "1",
	)
	for _, variable := range canonical.Environment {
		args = append(
			args,
			"--setenv",
			variable.Name,
			variable.Value,
		)
	}
	args = append(args, "--chdir", canonical.Workdir)

	if err := invocation.setConfidentialOptions(
		args,
		canonical.Command,
	); err != nil {
		return nil, fmt.Errorf(
			"retain confidential Bubblewrap sidecar arguments: %w",
			err,
		)
	}
	return invocation, nil
}
