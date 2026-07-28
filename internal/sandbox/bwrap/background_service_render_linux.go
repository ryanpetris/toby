//go:build linux

package bwrap

// Renders one descriptor-authoritative background service whose root is
// read-only before the fixed noninteractive payload starts.

import (
	"fmt"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/sandbox/layout"
)

// RenderBackgroundService validates and renders an agent-owned background
// service without reopening any host path.
func RenderBackgroundService(
	plan BackgroundServicePlan,
	sources BackgroundServiceSources,
) (result *Invocation, returnErr error) {
	if err := validateBackgroundServiceSourceCardinality(
		plan,
		sources,
	); err != nil {
		return nil, fmt.Errorf(
			"validate Bubblewrap background-service sources: %w",
			err,
		)
	}

	canonical := plan.Canonical()
	if err := canonical.Validate(); err != nil {
		return nil, fmt.Errorf(
			"validate Bubblewrap background-service plan: %w",
			err,
		)
	}
	if err := validateBackgroundServiceSources(
		canonical,
		sources,
	); err != nil {
		return nil, fmt.Errorf(
			"validate Bubblewrap background-service sources: %w",
			err,
		)
	}

	invocation := &Invocation{Mode: ExecutionNonInteractive}
	defer func() {
		if returnErr != nil {
			diagnostic.DiscardError(
				"background service rendering already failed",
				"close partial Bubblewrap background service invocation",
				invocation.Close(),
			)
			result = nil
		}
	}()

	rootFD, err := invocation.retain(
		sources.RootFS,
		"background-service rootfs",
	)
	if err != nil {
		return nil, err
	}
	upperFD, err := invocation.retain(
		sources.OverlayUpper,
		"background-service overlay upper",
	)
	if err != nil {
		return nil, err
	}
	workFD, err := invocation.retain(
		sources.OverlayWork,
		"background-service overlay work",
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
	args = append(args, "--cap-drop", "ALL")
	args = append(args,
		"--overlay-src", childFDPath(rootFD),
		"--overlay",
		childFDPath(upperFD),
		childFDPath(workFD),
		"/",
	)
	args = appendOverlayFDRegistrations(
		args,
		rootFD,
		upperFD,
		workFD,
	)
	args = append(args,
		"--proc", "/proc",
		"--dev", "/dev",
		"--tmpfs", "/tmp",
		"--tmpfs", "/run",
		"--dir", layout.Runtime,
		"--chmod", "0700", layout.Runtime,
	)

	for _, bind := range canonical.Binds {
		fd, err := invocation.retain(
			sources.Binds[bind.Target],
			"background-service bind "+bind.Target,
		)
		if err != nil {
			return nil, err
		}
		args = appendFDBind(args, bind.Access, fd, bind.Target)
	}
	if canonical.Runtime != nil {
		fd, err := invocation.retain(
			sources.Runtime,
			"background-service runtime",
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
		"--remount-ro", "/",
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
			"retain confidential Bubblewrap background-service arguments: %w",
			err,
		)
	}

	return invocation, nil
}
