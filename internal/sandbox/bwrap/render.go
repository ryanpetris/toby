package bwrap

// Renders one validated canonical Plan into direct Bubblewrap arguments and an
// independently owned, deterministic child-descriptor table.

import (
	"fmt"
	"io"
	"os"
	"strconv"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/sandbox/mount"
)

// Invocation is a rendered Bubblewrap command. Mode is the validated I/O mode
// the executor must use. ExtraFiles are owned by the Invocation and map by
// index to child descriptors beginning at 3. Close releases them after the
// command has started or when execution is abandoned.
type Invocation struct {
	Args       []string
	ExtraFiles []*os.File
	Mode       ExecutionMode

	// payloadArgIndex identifies the first command argument after Bubblewrap's
	// setup boundary so retry attempts can insert the trusted exec shim without
	// parsing option values.
	payloadArgIndex int

	// confidentialArguments identifies the sealed anonymous argument payload
	// rendered for agent-owned background services. Its descriptor occupies
	// the matching ExtraFiles index, while Args contains only Bubblewrap's
	// --args reference.
	confidentialArguments      bool
	confidentialArgumentsIndex int

	// allowOverlayReuseRetry is Run-owned replay-safety authority. Direct
	// callers cannot opt into retrying a command without prior run use.
	allowOverlayReuseRetry bool
}

var _ io.Closer = (*Invocation)(nil)

// Close releases every descriptor retained by the rendered invocation.
func (i *Invocation) Close() error {
	if i == nil {
		return nil
	}

	for index, file := range i.ExtraFiles {
		if file != nil {
			diagnostic.DiscardError(
				"releasing a rendered invocation descriptor is cleanup",
				"close Bubblewrap invocation descriptor",
				file.Close(),
				"descriptor_index", index,
			)
			i.ExtraFiles[index] = nil
		}
	}
	i.ExtraFiles = nil
	i.payloadArgIndex = 0
	i.confidentialArguments = false
	i.confidentialArgumentsIndex = 0
	i.allowOverlayReuseRetry = false

	return nil
}

// Render validates a detached canonical Plan, verifies exact source coverage,
// and duplicates every authoritative source descriptor into a caller-owned
// invocation. It never opens or stats a Plan path.
func Render(plan Plan, sources Sources) (result *Invocation, returnErr error) {
	if err := validateSourceCardinality(plan, sources); err != nil {
		return nil, fmt.Errorf("validate Bubblewrap sources: %w", err)
	}

	canonical := plan.Canonical()
	if err := canonical.Validate(); err != nil {
		return nil, fmt.Errorf("validate Bubblewrap plan: %w", err)
	}
	if err := validateSources(canonical, sources); err != nil {
		return nil, fmt.Errorf("validate Bubblewrap sources: %w", err)
	}

	invocation := &Invocation{}
	defer func() {
		if returnErr != nil {
			diagnostic.DiscardError(
				"Bubblewrap rendering already failed",
				"close partial Bubblewrap invocation",
				invocation.Close(),
			)
			result = nil
		}
	}()

	rootFSFD, err := invocation.retain(sources.RootFS, "rootfs")
	if err != nil {
		return nil, err
	}
	upperFD, err := invocation.retain(sources.OverlayUpper, "overlay upper")
	if err != nil {
		return nil, err
	}
	workFD, err := invocation.retain(sources.OverlayWork, "overlay work")
	if err != nil {
		return nil, err
	}

	uid, gid := canonical.Identity.HostUID, canonical.Identity.HostGID
	if canonical.Command.Root {
		uid, gid = 0, 0
	}
	args := namespaceArgs(uid, gid)
	args = append(args, "--cap-drop", "ALL")
	if canonical.Command.Capabilities == CapabilityRootLifecycle {
		// Namespace-root lifecycle commands need only the ownership, DAC, and
		// identity transitions used by tool installers. Mount, device, ptrace,
		// and network administration capabilities remain absent.
		for _, capability := range [...]string{
			"CAP_CHOWN",
			"CAP_DAC_OVERRIDE",
			"CAP_FOWNER",
			"CAP_FSETID",
			"CAP_SETGID",
			"CAP_SETUID",
		} {
			args = append(args, "--cap-add", capability)
		}
	}

	args = append(args,
		"--overlay-src", childFDPath(rootFSFD),
		"--overlay",
		childFDPath(upperFD),
		childFDPath(workFD),
		"/",
	)
	args = appendOverlayFDRegistrations(args, rootFSFD, upperFD, workFD)
	args = append(args,
		"--proc", "/proc",
		"--dev", "/dev",
		"--tmpfs", "/tmp",
		"--dir", "/run",
		"--dir", layout.Runtime,
		"--chmod", "0700", layout.Runtime,
		"--dir", layout.Root,
		"--dir", layout.Home,
		"--dir", layout.Workspace,
		"--dir", layout.Bin,
	)

	homeFD, err := invocation.retain(sources.Home, "private home")
	if err != nil {
		return nil, err
	}
	args = appendFDBind(args, mount.AccessRegular, homeFD, layout.Home)

	for _, entry := range canonical.ManagedDirectories {
		fd, err := invocation.retain(
			sources.ManagedDirectories[entry.Key],
			"managed-directory "+entry.Key.String(),
		)
		if err != nil {
			return nil, err
		}
		args = appendFDBind(args, entry.Access, fd, entry.Target)
	}

	for _, project := range canonical.Projects {
		fd, err := invocation.retain(
			sources.Projects[project.Name],
			"project "+project.Name,
		)
		if err != nil {
			return nil, err
		}
		access := mount.AccessRegular
		if project.ReadOnly {
			access = mount.AccessReadOnly
		}
		args = appendFDBind(args, access, fd, project.Target)
	}

	for _, bind := range canonical.Binds {
		fd, err := invocation.retain(
			sources.Binds[bind.Target],
			"external bind "+bind.Target,
		)
		if err != nil {
			return nil, err
		}
		args = appendFDBind(args, bind.Access, fd, bind.Target)
	}

	for _, asset := range canonical.RuntimeAssets {
		fd, err := invocation.retain(
			sources.RuntimeAssets[asset.Target],
			"runtime asset "+asset.Target,
		)
		if err != nil {
			return nil, err
		}
		args = appendFDBind(args, asset.Access, fd, asset.Target)
	}

	binaryFD, err := invocation.retain(sources.SandboxBinary, "sandbox helper")
	if err != nil {
		return nil, err
	}
	args = appendFDBind(
		args,
		mount.AccessReadOnly,
		binaryFD,
		canonical.SandboxBinary.Target,
	)

	args = append(args,
		"--clearenv",
		"--setenv", "HOME", layout.Home,
		"--setenv", "TOBY_SANDBOX", "1",
	)
	for _, variable := range canonical.Environment {
		args = append(args, "--setenv", variable.Name, variable.Value)
	}
	args = append(args, "--chdir", canonical.Workdir, "--")
	invocation.payloadArgIndex = len(args)
	args = append(args, canonical.Command.Argv...)

	invocation.Args = args
	invocation.Mode = canonical.Command.Mode
	return invocation, nil
}

// appendOverlayFDRegistrations makes Bubblewrap consume and close overlay
// descriptors as setup-operation FDs. Their stacked temporary mounts are
// hidden by the later /dev mount before the payload starts. Bubblewrap still
// canonicalizes overlay proc-FD paths before mount(2), so the overlay itself is
// not FD-atomic against a concurrent same-user path swap.
func appendOverlayFDRegistrations(args []string, descriptors ...int) []string {
	for _, fd := range descriptors {
		args = append(
			args,
			"--ro-bind-fd", strconv.Itoa(fd), "/dev",
		)
	}
	return args
}

// appendAnonymousRootOverlay renders a descriptor-backed immutable lower with
// an anonymous upper that Bubblewrap discards when the sandbox exits. Callers
// must remount the completed root read-only after installing their child
// mounts if the payload is not allowed to mutate it.
func appendAnonymousRootOverlay(args []string, rootFD int) []string {
	args = append(args,
		"--overlay-src", childFDPath(rootFD),
		"--tmp-overlay", "/",
	)

	return appendOverlayFDRegistrations(args, rootFD)
}

func (i *Invocation) retain(source *os.File, label string) (int, error) {
	duplicate, err := duplicateDescriptor(source, label)
	if err != nil {
		return 0, err
	}

	childFD := childExtraFileBaseFD + len(i.ExtraFiles)
	i.ExtraFiles = append(i.ExtraFiles, duplicate)

	return childFD, nil
}

func appendFDBind(
	args []string,
	access mount.Access,
	fd int,
	target string,
) []string {
	option := "--bind-fd"
	if access == mount.AccessReadOnly {
		option = "--ro-bind-fd"
	}

	return append(args, option, strconv.Itoa(fd), target)
}
