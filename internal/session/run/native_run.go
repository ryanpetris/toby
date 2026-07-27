//go:build linux

package run

// Assembles one descriptor-authoritative Bubblewrap run from leased OCI,
// native-storage, agent-lease, generated-file, runtime-asset, project, and
// bind inputs.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/oci"
	"petris.dev/toby/internal/runtimeassets"
	"petris.dev/toby/internal/sandbox/bwrap"
	"petris.dev/toby/internal/sandbox/mount"
	"petris.dev/toby/internal/sandboxgateway"
	"petris.dev/toby/internal/socketrelay"
	"petris.dev/toby/internal/storage"
	"petris.dev/toby/internal/storage/safefs"
	"petris.dev/toby/internal/toolfiles"
)

// NativeProject pairs one deterministic project plan entry with the exact
// caller-owned directory descriptor that authorizes it.
type NativeProject struct {
	Input  bwrap.ProjectInput
	Source *os.File
}

// NativeBind pairs one selected external-bind declaration with its exact
// caller-owned source and direct-parent descriptors plus the resolved
// basename beneath that parent. Optional declarations that are absent are
// omitted.
type NativeBind struct {
	Bind         mount.Bind
	Source       *os.File
	Parent       *os.File
	ResolvedName string
}

// PreparedImage is the leased immutable OCI rootfs surface consumed by one
// native run.
type PreparedImage interface {
	io.Closer
	// RootfsPath returns the prepared root filesystem path.
	RootfsPath() string
	// RootfsFile opens the prepared root filesystem.
	RootfsFile() (*os.File, error)
	// Spec returns the prepared image specification.
	Spec() oci.Spec
}

var _ PreparedImage = (*oci.Prepared)(nil)

// PrivateHome is the retained native private-home capability consumed by one
// run.
type PrivateHome interface {
	io.Closer
	// Identity returns the canonical home identity.
	Identity() storage.HomeIdentity
	// HostPath returns the home data path.
	HostPath() string
	// File opens the retained home directory.
	File() (*os.File, error)
}

var _ PrivateHome = (*storage.HomeHandle)(nil)

// ManagedDirectory is one retained resolved native mount capability.
type ManagedDirectory interface {
	io.Closer
	// Entry returns the resolved mount entry.
	Entry() mount.Entry
	// File opens the retained mount directory.
	File() (*os.File, error)
}

var _ ManagedDirectory = (*storage.ManagedHandle)(nil)

// NativeRunInput contains the already-resolved capabilities needed to create
// one run. Prepared, Home, and Managed ownership transfers to NativeRun only
// after successful construction. Directories and SocketRelays are run-specific
// transaction resources: after input validation succeeds, construction
// consumes and closes them on failure or transfers them to NativeRun on
// success. Resource leases remain owned by the launch client.
type NativeRunInput struct {
	Prepared PreparedImage
	Home     PrivateHome
	Managed  []ManagedDirectory

	Directories *bwrap.RunDirectories

	Projects []NativeProject
	Binds    []NativeBind

	ProtectedRoots bwrap.ProtectedRoots
	RuntimeRoot    *safefs.Directory
	RuntimeAssets  *runtimeassets.Registry
	SandboxGateway *sandboxgateway.Capability
	SocketRelays   *socketrelay.Set
	ToolFiles      []toolfiles.File
	Logger         *diagnostic.Logger

	SandboxBinaryPath string
	SandboxBinary     *os.File
	Workdir           string
	Identity          bwrap.Identity

	ToolSandbox *bwrap.ToolSandbox
	Executor    bwrap.ProcessExecutor
}

// NativeRun owns one Bubblewrap run and the OCI/native-storage leases that
// must remain live until its last process and overlay are gone.
type NativeRun struct {
	mu sync.Mutex

	bubblewrap   *bwrap.Run
	socketRelays *socketrelay.Set
	prepared     PreparedImage
	home         PrivateHome
	managed      []ManagedDirectory
	logger       *diagnostic.Logger
	closed       bool
}

var _ io.Closer = (*NativeRun)(nil)

// NewNativeRun writes generated files, materializes transient assets, builds a
// complete plan, retains its exact sources, and attaches the tool-facing
// sandbox. No application or private-home lock is acquired.
func NewNativeRun(
	ctx context.Context,
	input NativeRunInput,
) (result *NativeRun, returnErr error) {
	if ctx == nil {
		return nil, fmt.Errorf("create native run: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateNativeRunInput(input); err != nil {
		return nil, err
	}

	directories := input.Directories
	socketRelays := input.SocketRelays
	var runOwner io.Closer = directories
	defer func() {
		if returnErr == nil {
			return
		}
		if socketRelays != nil {
			input.Logger.DebugError(
				"close socket relays after run assembly failure",
				socketRelays.Close(),
			)
		}
		if runOwner != nil {
			input.Logger.DebugError(
				"close run storage after assembly failure",
				runOwner.Close(),
			)
		}
	}()

	sources, closeSources, err := nativeRunSources(input, directories)
	if err != nil {
		return nil, err
	}
	sourcesOpen := true
	defer func() {
		if sourcesOpen {
			input.Logger.DebugError(
				"close run assembly sources",
				closeSources(),
			)
		}
	}()

	var assetSet *runtimeassets.Set
	var assets []bwrap.RuntimeAsset
	if input.RuntimeAssets != nil {
		assetSet, err = input.RuntimeAssets.Materialize(
			input.RuntimeRoot,
			input.Logger,
		)
		if err != nil {
			return nil, fmt.Errorf("materialize native run assets: %w", err)
		}
		defer func() {
			if assetSet != nil {
				input.Logger.DebugError(
					"close materialized runtime assets",
					assetSet.Close(),
				)
			}
		}()

		assets = assetSet.RuntimeAssets()
		assetSources, err := assetSet.Sources()
		if err != nil {
			return nil, fmt.Errorf("access native run assets: %w", err)
		}
		for target, source := range assetSources {
			sources.RuntimeAssets[target] = source
		}
	}
	if input.SandboxGateway != nil {
		assets = append(assets, bwrap.RuntimeAsset{
			HostPath: input.SandboxGateway.HostSocket(),
			Target:   input.SandboxGateway.SandboxSocket(),
			Access:   mount.AccessDev,
		})
	}
	if input.SocketRelays != nil {
		assets = append(assets, input.SocketRelays.RuntimeAssets()...)
	}

	basePlan, err := buildNativePlan(input, directories, assets, nil)
	if err != nil {
		return nil, err
	}
	generated, err := toolfiles.NewWriter(input.Logger).Write(
		basePlan,
		sources,
		input.ToolFiles,
	)
	if err != nil {
		return nil, err
	}
	plan, err := buildNativePlan(input, directories, assets, generated)
	if err != nil {
		return nil, err
	}

	bubblewrapRun, err := bwrap.NewRun(
		plan,
		sources,
		directories,
		input.Executor,
		input.Logger,
	)
	if err != nil {
		return nil, fmt.Errorf("create Bubblewrap run: %w", err)
	}
	runOwner = bubblewrapRun

	// Bubblewrap retained every source and now owns directories. Release the
	// assembly-only descriptors while leaving materialized path names beneath
	// the run directory available until Bubblewrap setup has consumed them.
	directories = nil
	input.Logger.DebugError(
		"close run assembly sources",
		closeSources(),
	)
	sourcesOpen = false
	if assetSet != nil {
		input.Logger.DebugError(
			"release runtime-asset materialization descriptors",
			assetSet.TransferStorageCleanup(),
		)
		assetSet = nil
	}

	if err := input.ToolSandbox.Attach(bubblewrapRun); err != nil {
		return nil, err
	}

	// Higher-level leases transfer only after complete successful attachment.
	result = &NativeRun{
		bubblewrap:   bubblewrapRun,
		socketRelays: socketRelays,
		prepared:     input.Prepared,
		home:         input.Home,
		managed:      append([]ManagedDirectory(nil), input.Managed...),
		logger:       input.Logger,
	}
	socketRelays = nil
	runOwner = nil

	return result, nil
}

// Close stops using retained sources, removes the run overlay, and releases
// higher native-storage and OCI leases.
func (r *NativeRun) Close() error {
	if r == nil {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}

	if r.socketRelays != nil {
		r.logger.DebugError("close socket relays", r.socketRelays.Close())
		r.socketRelays = nil
	}
	if r.bubblewrap != nil {
		r.logger.DebugError(
			"close Bubblewrap run",
			r.bubblewrap.Close(),
		)
		r.bubblewrap = nil
	}

	for index := len(r.managed) - 1; index >= 0; index-- {
		if r.managed[index] != nil {
			r.logger.DebugError(
				"close tool volume",
				r.managed[index].Close(),
			)
			r.managed[index] = nil
		}
	}
	r.managed = nil
	if r.home != nil {
		r.logger.DebugError("close private home", r.home.Close())
		r.home = nil
	}
	if r.prepared != nil {
		r.logger.DebugError("close prepared OCI image", r.prepared.Close())
		r.prepared = nil
	}
	r.closed = true

	return nil
}

func validateNativeRunInput(input NativeRunInput) error {
	switch {
	case input.Prepared == nil:
		return fmt.Errorf("create native run: prepared OCI image is required")
	case input.Home == nil:
		return fmt.Errorf("create native run: private home is required")
	case input.ProtectedRoots.ImageStore == nil:
		return fmt.Errorf(
			"create native run: OCI image-store root descriptor is required",
		)
	case input.ProtectedRoots.PersistentData == nil:
		return fmt.Errorf(
			"create native run: Toby persistent-data root descriptor is required",
		)
	case input.ProtectedRoots.RunStorage == nil:
		return fmt.Errorf(
			"create native run: Bubblewrap run-storage root descriptor is required",
		)
	case input.ProtectedRoots.Runtime == nil:
		return fmt.Errorf(
			"create native run: Toby runtime root descriptor is required",
		)
	case input.SandboxBinary == nil:
		return fmt.Errorf("create native run: sandbox helper descriptor is required")
	case input.SandboxBinaryPath == "":
		return fmt.Errorf("create native run: sandbox helper diagnostic path is required")
	case input.ToolSandbox == nil:
		return fmt.Errorf("create native run: tool sandbox is required")
	case input.Directories == nil:
		return fmt.Errorf("create native run: run directories are required")
	case input.Executor == nil:
		return fmt.Errorf("create native run: Bubblewrap executor is required")
	case input.RuntimeAssets != nil && input.RuntimeRoot == nil:
		return fmt.Errorf(
			"create native run: runtime root is required when assets are registered",
		)
	}
	if input.Prepared.RootfsPath() == "" {
		return fmt.Errorf("create native run: prepared OCI image is closed")
	}
	if input.Home.HostPath() == "" {
		return fmt.Errorf("create native run: private home is closed")
	}
	if err := input.ToolSandbox.ValidateImageEnvironment(
		input.Prepared.Spec().Runtime.Environment,
	); err != nil {
		return fmt.Errorf("create native run: %w", err)
	}

	return nil
}

func buildNativePlan(
	input NativeRunInput,
	directories *bwrap.RunDirectories,
	assets []bwrap.RuntimeAsset,
	generated []bwrap.GeneratedFile,
) (bwrap.Plan, error) {
	spec := input.Prepared.Spec()
	projects := make([]bwrap.ProjectInput, len(input.Projects))
	for index, project := range input.Projects {
		projects[index] = project.Input
	}
	binds := make([]mount.Bind, len(input.Binds))
	for index, bind := range input.Binds {
		binds[index] = bind.Bind
	}
	managed := make([]mount.Entry, len(input.Managed))
	for index, handle := range input.Managed {
		if handle == nil {
			return bwrap.Plan{}, fmt.Errorf(
				"create native run: managed handle %d is nil",
				index,
			)
		}
		managed[index] = handle.Entry()
	}

	return bwrap.BuildPlan(bwrap.PlanInput{
		RunID: directories.ID(),
		RootFS: bwrap.RootFS{
			Digest: spec.Manifest.Digest.String(),
			Path:   input.Prepared.RootfsPath(),
		},
		Overlay:            directories.Overlay(),
		Home:               bwrap.Home{ID: input.Home.Identity().ID, HostPath: input.Home.HostPath()},
		Projects:           projects,
		ManagedDirectories: managed,
		Binds:              binds,
		RuntimeAssets:      assets,
		GeneratedFiles:     generated,
		SandboxBinaryPath:  input.SandboxBinaryPath,
		Workdir:            input.Workdir,
		Environment:        input.ToolSandbox.EnvironmentVariables(),
		Identity:           input.Identity,
		CommandArgv:        []string{"/bin/true"},
		ExecutionMode:      bwrap.ExecutionNonInteractive,
	})
}

func nativeRunSources(
	input NativeRunInput,
	directories *bwrap.RunDirectories,
) (sources bwrap.Sources, close func() error, resultErr error) {
	var opened []*os.File
	retain := func(file *os.File, label string) (*os.File, error) {
		if file == nil {
			return nil, fmt.Errorf("%s descriptor is nil", label)
		}
		duplicate, err := duplicateFile(file)
		if err != nil {
			return nil, fmt.Errorf("duplicate %s descriptor: %w", label, err)
		}
		opened = append(opened, duplicate)
		return duplicate, nil
	}
	close = func() error {
		var closeErr error
		for index, file := range opened {
			if file != nil {
				closeErr = errors.Join(closeErr, file.Close())
				opened[index] = nil
			}
		}
		return closeErr
	}
	defer func() {
		if resultErr != nil {
			input.Logger.DebugError(
				"close partially retained run sources",
				close(),
			)
		}
	}()

	var err error
	sources.ProtectedRoots.ImageStore, err = retain(
		input.ProtectedRoots.ImageStore,
		"OCI image-store root",
	)
	if err != nil {
		return bwrap.Sources{}, close, err
	}
	sources.ProtectedRoots.PersistentData, err = retain(
		input.ProtectedRoots.PersistentData,
		"Toby persistent-data root",
	)
	if err != nil {
		return bwrap.Sources{}, close, err
	}
	sources.ProtectedRoots.RunStorage, err = retain(
		input.ProtectedRoots.RunStorage,
		"Bubblewrap run-storage root",
	)
	if err != nil {
		return bwrap.Sources{}, close, err
	}
	sources.ProtectedRoots.Runtime, err = retain(
		input.ProtectedRoots.Runtime,
		"Toby runtime root",
	)
	if err != nil {
		return bwrap.Sources{}, close, err
	}
	sources.RootFS, err = input.Prepared.RootfsFile()
	if err != nil {
		return bwrap.Sources{}, close, fmt.Errorf("open OCI rootfs source: %w", err)
	}
	opened = append(opened, sources.RootFS)
	sources.OverlayUpper, err = directories.UpperFile()
	if err != nil {
		return bwrap.Sources{}, close, fmt.Errorf("open run upper source: %w", err)
	}
	opened = append(opened, sources.OverlayUpper)
	sources.OverlayWork, err = directories.WorkFile()
	if err != nil {
		return bwrap.Sources{}, close, fmt.Errorf("open run work source: %w", err)
	}
	opened = append(opened, sources.OverlayWork)
	sources.Home, err = input.Home.File()
	if err != nil {
		return bwrap.Sources{}, close, fmt.Errorf("open private-home source: %w", err)
	}
	opened = append(opened, sources.Home)

	sources.ManagedDirectories = make(map[mount.Key]*os.File, len(input.Managed))
	for _, handle := range input.Managed {
		if handle == nil {
			return bwrap.Sources{}, close, fmt.Errorf(
				"open managed-directory source: handle is nil",
			)
		}
		entry := handle.Entry()
		source, err := handle.File()
		if err != nil {
			return bwrap.Sources{}, close, fmt.Errorf(
				"open managed-directory source %s: %w",
				entry.Key,
				err,
			)
		}
		opened = append(opened, source)
		sources.ManagedDirectories[entry.Key] = source
	}

	sources.Projects = make(map[string]*os.File, len(input.Projects))
	for _, project := range input.Projects {
		source, err := retain(
			project.Source,
			"project "+project.Input.Name,
		)
		if err != nil {
			return bwrap.Sources{}, close, err
		}
		sources.Projects[project.Input.Name] = source
	}

	sources.Binds = make(map[string]*os.File, len(input.Binds))
	sources.BindParents = make(map[string]*os.File, len(input.Binds))
	sources.BindNames = make(map[string]string, len(input.Binds))
	for _, bind := range input.Binds {
		source, err := retain(bind.Source, "bind "+bind.Bind.Target)
		if err != nil {
			return bwrap.Sources{}, close, err
		}
		parent, err := retain(
			bind.Parent,
			"bind "+bind.Bind.Target+" parent",
		)
		if err != nil {
			return bwrap.Sources{}, close, err
		}
		sources.Binds[bind.Bind.Target] = source
		sources.BindParents[bind.Bind.Target] = parent
		sources.BindNames[bind.Bind.Target] = bind.ResolvedName
	}
	sources.RuntimeAssets = make(map[string]*os.File)
	if input.SocketRelays != nil {
		relaySources, err := input.SocketRelays.Sources()
		if err != nil {
			return bwrap.Sources{}, close, fmt.Errorf(
				"open socket relay capabilities: %w",
				err,
			)
		}
		for target, source := range relaySources {
			sources.RuntimeAssets[target] = source
		}
	}
	if input.SandboxGateway != nil {
		source, err := input.SandboxGateway.File()
		if err != nil {
			return bwrap.Sources{}, close, fmt.Errorf(
				"open sandbox gateway capability: %w",
				err,
			)
		}
		opened = append(opened, source)
		sources.RuntimeAssets[input.SandboxGateway.SandboxSocket()] = source
	}
	sources.SandboxBinary, err = retain(input.SandboxBinary, "sandbox helper")
	if err != nil {
		return bwrap.Sources{}, close, err
	}

	return sources, close, nil
}

func duplicateFile(file *os.File) (*os.File, error) {
	raw, err := file.SyscallConn()
	if err != nil {
		return nil, err
	}

	var duplicate int
	var duplicateErr error
	if err := raw.Control(func(descriptor uintptr) {
		duplicate, duplicateErr = unix.FcntlInt(
			descriptor,
			unix.F_DUPFD_CLOEXEC,
			0,
		)
	}); err != nil {
		return nil, err
	}
	if duplicateErr != nil {
		return nil, duplicateErr
	}

	return os.NewFile(uintptr(duplicate), file.Name()), nil
}
