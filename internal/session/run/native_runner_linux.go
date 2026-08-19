//go:build linux

package run

// Orchestrates one production Linux launch through OCI preparation, native
// storage, agent acquisition, tool-file publication, and direct Bubblewrap
// foreground execution.

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"petris.dev/toby/internal/agent"
	agentclient "petris.dev/toby/internal/agent/client"
	"petris.dev/toby/internal/approval"
	"petris.dev/toby/internal/config"
	appconfig "petris.dev/toby/internal/config/app"
	launchconfig "petris.dev/toby/internal/config/launch"
	mcpconfig "petris.dev/toby/internal/config/mcp"
	"petris.dev/toby/internal/config/mcpresource"
	"petris.dev/toby/internal/config/session"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/diagnostic/exitcode"
	"petris.dev/toby/internal/diagnostic/warning"
	"petris.dev/toby/internal/hostaction"
	"petris.dev/toby/internal/hostaction/methods/git"
	"petris.dev/toby/internal/lifecycle"
	"petris.dev/toby/internal/oci"
	"petris.dev/toby/internal/oci/image"
	"petris.dev/toby/internal/oci/imagesource"
	"petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/sandbox/bwrap"
	"petris.dev/toby/internal/shutdown"
	"petris.dev/toby/internal/status"
	"petris.dev/toby/internal/storage/safefs"
	"petris.dev/toby/internal/toolfiles"
	"petris.dev/toby/internal/tools"
)

// NativeRunnerParams supplies the statically composed process services and
// host streams used by one CLI process.
type NativeRunnerParams struct {
	Paths         config.Paths
	Registry      *tools.Registry
	BaseConfig    *appconfig.Service
	LaunchConfig  *appconfig.LaunchHolder
	SessionConfig *sessionconfig.Holder
	Agent         *agent.Client
	Diagnostics   *diagnostic.Service
	Lifecycle     *lifecycle.Runner
	Sandbox       *bwrap.ToolService
	Git           *git.Service
	Approval      *approval.Service
	Status        *status.Service
	Warnings      *warning.Service
	Shutdown      *shutdown.Service
	Stdin         io.Reader
	Stdout        io.Writer
	Stderr        io.Writer
}

// NativeRunner owns the process-local launch orchestration dependencies.
type NativeRunner struct {
	paths         config.Paths
	registry      *tools.Registry
	baseConfig    *appconfig.Service
	launchConfig  *appconfig.LaunchHolder
	sessionConfig *sessionconfig.Holder
	agent         *agent.Client
	diagnostics   *diagnostic.Service
	logger        *diagnostic.Logger
	lifecycle     *lifecycle.Runner
	sandbox       *bwrap.ToolService
	git           *git.Service
	approval      *approval.Service
	status        *status.Service
	warnings      *warning.Service
	shutdown      *shutdown.Service
	stdin         io.Reader
	stdout        io.Writer
	stderr        io.Writer
}

var _ Runner = (*NativeRunner)(nil)

// NewNativeRunner constructs the Linux production runner. Dynamic run objects
// remain ordinary values owned by Run rather than Fx graph nodes.
func NewNativeRunner(params NativeRunnerParams) *NativeRunner {
	return &NativeRunner{
		paths:         params.Paths,
		registry:      params.Registry,
		baseConfig:    params.BaseConfig,
		launchConfig:  params.LaunchConfig,
		sessionConfig: params.SessionConfig,
		agent:         params.Agent,
		diagnostics:   params.Diagnostics,
		logger:        params.Diagnostics.Logger("session.run"),
		lifecycle:     params.Lifecycle,
		sandbox:       params.Sandbox,
		git:           params.Git,
		approval:      params.Approval,
		status:        params.Status,
		warnings:      params.Warnings,
		shutdown:      params.Shutdown,
		stdin:         params.Stdin,
		stdout:        params.Stdout,
		stderr:        params.Stderr,
	}
}

// Run executes one complete native launch and releases every foreground,
// agent, overlay, storage, and OCI lease before returning.
func (r *NativeRunner) Run(
	ctx context.Context,
	opts *tools.Options,
	overrides appconfig.LaunchOverrides,
	extra []string,
	requestedTools []string,
	primary string,
) (returnErr error) {
	if err := r.validate(); err != nil {
		return err
	}
	if ctx == nil {
		return fmt.Errorf("native launch context is nil")
	}
	if err := r.shutdown.BeginStartup(); err != nil {
		return err
	}
	defer func() {
		if err := r.shutdown.EndStartup(); returnErr == nil && err != nil {
			returnErr = err
		}
	}()
	if opts == nil {
		opts = &tools.Options{}
	}
	options := nativeLaunchOptions(*opts, primary)

	effective := r.baseConfig.WithOverrides(overrides)
	if err := effective.Sandbox().Validate(); err != nil {
		return err
	}
	r.launchConfig.SetCurrent(effective)
	effective.EmitPendingWarnings(r.warnings)
	settings := effective.Settings()
	if options.Quiet && settings.DebugEnabled() {
		return exitcode.New(
			2,
			"--debug and --quiet are mutually exclusive",
		)
	}
	if err := r.status.Begin(status.Options{
		Debug: settings.DebugEnabled(),
		Quiet: options.Quiet,
	}); err != nil {
		return err
	}
	defer func() {
		returnErr = r.status.Finish(returnErr)
	}()
	launchOperation, _, _ := r.startOperation(
		"Preparing launch",
	)

	hostActionHandler := &nativeHostActionHandler{}
	agentSession, err := r.agent.Connect(
		ctx,
		hostActionHandler,
	)
	if err != nil {
		return err
	}
	var cleanupOnce sync.Once
	var cleanupCtx context.Context
	var cancelCleanup context.CancelFunc
	cleanupContext := func() context.Context {
		cleanupOnce.Do(func() {
			cleanupCtx, cancelCleanup = r.shutdown.CleanupContext()
		})
		return cleanupCtx
	}
	defer func() {
		if cancelCleanup != nil {
			cancelCleanup()
		}
	}()
	defer func() {
		r.logCleanup(
			"close agent session",
			agentSession.CloseContext(cleanupContext()),
		)
	}()
	defer func() {
		r.logCleanup(
			"acknowledge agent shutdown",
			agentSession.AcknowledgeStopping(cleanupContext()),
		)
	}()
	go r.followAgentShutdown(ctx, agentSession)
	resourceConfiguration, err := effective.ResolveResources(r.warnings)
	if err != nil {
		return err
	}

	if err := prepareConfiguredProjects(
		r.warnings,
		r.paths.Home,
		&options,
	); err != nil {
		return err
	}
	if len(options.Projects) == 0 {
		project, err := launchconfig.ResolveDirectLaunchProject(
			r.paths,
			options,
			settings.AllowExternalProjectsEnabled(),
		)
		if err != nil {
			return err
		}
		options.Projects = []tools.ProjectMount{project}
	}

	toolset, err := r.registry.Build(requestedTools, primary)
	if err != nil {
		return err
	}
	if toolset.Primary() == nil {
		return fmt.Errorf("native launch requires a primary tool")
	}

	mode := nativeForegroundMode(
		settings.ManagedTerminalEnabled(),
		r.stdin,
		r.stdout,
		r.stderr,
	)
	warnIfNativeAutoDeny(
		r.warnings,
		effective,
		mode,
	)

	launchOperation.Finish(nil)
	if err := r.shutdown.Checkpoint(); err != nil {
		return err
	}
	executor, err := bwrap.NewExecutor(bwrap.ExecutorOptions{
		ExternalInterrupts: true,
		Logger:             r.logger,
	})
	if err != nil {
		return err
	}
	defer func() {
		r.logCleanup("close Bubblewrap executor", executor.Close())
	}()
	uid := os.Geteuid()
	gid := os.Getegid()
	identity := bwrap.Identity{HostUID: uid, HostGID: gid}

	sandboxConfig := effective.Sandbox()
	if sandboxConfig.Pull == "" {
		sandboxConfig.Pull = image.PullIfMissing
	}

	sandboxResource, err := r.prepareNativeOCIResources(
		ctx,
		agentSession,
		sandboxConfig,
		&resourceConfiguration,
		effective.Profile(),
		options.Projects[0].Name,
	)
	if err != nil {
		return err
	}
	if err := r.shutdown.Checkpoint(); err != nil {
		return err
	}

	ociStore, err := oci.NewStore(
		r.paths,
		r.diagnostics,
	)
	if err != nil {
		return err
	}
	defer func() {
		r.logCleanup("close OCI image store", ociStore.Close())
	}()
	imageStoreRoot, err := ociStore.ImageStoreFile()
	if err != nil {
		return err
	}
	defer func() {
		r.logCleanup("close OCI image-store root", imageStoreRoot.Close())
	}()

	prepared, err := ociStore.Prepare(ctx, oci.Request{
		Source:    imagesource.Registry,
		Reference: sandboxResource.Reference,
		Platform: ocispec.Platform{
			OS:           "linux",
			Architecture: runtime.GOARCH,
		},
		PullPolicy: image.PullNever,
	})
	if err != nil {
		return err
	}
	defer func() {
		if prepared != nil {
			r.logCleanup("close prepared OCI image", prepared.Close())
		}
	}()
	projects, projectView, err := openNativeProjects(
		options.Projects,
		r.paths.ProjectRoot,
		r.logger,
	)
	if err != nil {
		return err
	}
	defer func() {
		r.logCleanup(
			"close project directory capabilities",
			closeNativeProjects(projects),
		)
	}()

	workdir, err := resolveNativeWorkdir(options.Workdir, projectView)
	if err != nil {
		return err
	}

	toolSandbox, err := bwrap.NewToolSandbox(bwrap.ToolSandboxOptions{
		Projects:         projectView,
		ImageEnvironment: prepared.Spec().Runtime.Environment,
		TerminalType:     nativeTerminalType(mode),
		ForegroundStreams: bwrap.ProcessIO{
			Stdin:                 r.stdin,
			Stdout:                r.stdout,
			Stderr:                r.stderr,
			RegisterPrompter:      r.approval.SetPrompter,
			RegisterSignalHandler: r.shutdown.RegisterForeground,
		},
		StartLifecycleOperation: r.startLifecycleOperation,
		ForegroundMode:          mode,
		RevealHiddenOutput:      r.status.RevealsHiddenOutput(),
	})
	if err != nil {
		return err
	}
	if err := r.sandbox.Set(toolSandbox); err != nil {
		return err
	}
	defer func() {
		r.approval.SetPrompter(nil)
		r.logCleanup(
			"clear active tool sandbox",
			r.sandbox.Clear(toolSandbox),
		)
	}()

	toolOperation, _, toolStderr := r.startOperation(
		"Preparing tools",
	)
	lctx := lifecycle.Context{
		Options:          &options,
		Stderr:           toolStderr,
		SuppressWarnings: settings.SuppressWarnings,
		Checkpoint:       r.shutdown.Checkpoint,
	}
	if err := r.lifecycle.PrepareHost(ctx, toolset, lctx); err != nil {
		return err
	}
	toolOperation.Finish(nil)
	if err := r.shutdown.Checkpoint(); err != nil {
		return err
	}

	binds, err := openNativeBinds(toolSandbox.Binds(), r.logger)
	if err != nil {
		return err
	}
	defer func() {
		r.logCleanup(
			"close external bind capabilities",
			closeNativeBinds(binds),
		)
	}()

	launchStorage, err := r.prepareNativeLaunchStorage(
		ctx,
		options.Env,
		effective.Profile(),
		effective.ToolProfiles(),
		prepared,
		toolSandbox.MountRequests(),
		nativeOccupiedTargets(projectView, binds),
	)
	if err != nil {
		return err
	}
	volumeStore := launchStorage.volumeStore
	defer func() {
		r.logCleanup("close volume store", volumeStore.Close())
	}()
	persistentDataRoot := launchStorage.persistentDataRoot
	defer func() {
		r.logCleanup(
			"close persistent data root",
			persistentDataRoot.Close(),
		)
	}()
	home := launchStorage.home
	defer func() {
		if home != nil {
			r.logCleanup("close private home", home.Close())
		}
	}()
	managed := launchStorage.managed
	defer func() {
		if managed != nil {
			r.logCleanup(
				"close tool volumes",
				closeManagedHandles(managed),
			)
		}
	}()
	runStorage := launchStorage.runStorage
	defer func() {
		r.logCleanup("close Bubblewrap run storage", runStorage.Close())
	}()
	runStorageRoot := launchStorage.runStorageRoot
	defer func() {
		r.logCleanup(
			"close Bubblewrap run-storage root",
			runStorageRoot.Close(),
		)
	}()
	directories := launchStorage.directories
	defer func() {
		if directories != nil {
			r.logCleanup("close Bubblewrap run directories", directories.Close())
		}
	}()
	if err := r.shutdown.Checkpoint(); err != nil {
		return err
	}

	entries := nativeManagedEntries(managed)
	selectedBinds := nativeBindPlans(binds)
	snapshot, err := buildNativeSessionSnapshot(nativeSnapshotInput{
		Debug:        settings.DebugEnabled(),
		Environment:  options.Env,
		Profile:      home.Identity().Profile,
		RootFSDigest: prepared.Spec().Manifest.Digest.String(),
		Workdir:      workdir,
		Registry:     r.registry,
		Toolset:      toolset,
		Projects:     projectView,
		Mounts:       entries,
		Binds:        selectedBinds,
		MCP: mcpconfig.Config{
			Servers: resourceConfiguration.MCPs,
		},
	})
	if err != nil {
		return err
	}
	r.git.SetResolver(toolSandbox)
	r.git.SetApprover(r.approval)
	defer func() {
		r.git.SetResolver(nil)
		r.git.SetApprover(nil)
	}()
	router, err := hostaction.NewRouter([]hostaction.Capability{r.git})
	if err != nil {
		return err
	}
	hostActionHandler.SetRouter(router)
	backgroundOperation := r.status.StartOperation("Registering resources")

	resources, err := acquireNativeResources(
		ctx,
		nativeResourceInput{
			RunID:          directories.ID(),
			Paths:          r.paths,
			Session:        agentSession,
			Snapshot:       snapshot,
			Configuration:  effective,
			Resources:      resourceConfiguration,
			Logger:         r.logger,
			Warnings:       r.warnings,
			CleanupContext: cleanupContext,
			Identities: mcpresource.ScopeIdentities{
				Home:    home.Identity().ID,
				Project: nativeProjectScopeIdentity(projectView[0]),
				Run:     directories.ID(),
			},
		},
	)
	if err != nil {
		return err
	}
	backgroundOperation.Finish(nil)
	if err := r.shutdown.Checkpoint(); err != nil {
		return err
	}
	defer func() {
		r.logCleanup(
			"close launch resources",
			resources.CloseContext(cleanupContext()),
		)
	}()

	r.sessionConfig.Set(resources.Config)
	configOperation, _, configStderr := r.startOperation(
		"Configuring tools",
	)
	lctx.Stderr = configStderr
	if err := r.lifecycle.Configure(ctx, toolset, lctx, extra); err != nil {
		return err
	}
	configOperation.Finish(nil)
	if err := r.shutdown.Checkpoint(); err != nil {
		return err
	}

	files, err := lifecycle.ToolFiles(
		toolset,
		toolfiles.Ownership{UID: uid, GID: gid},
	)
	if err != nil {
		return err
	}
	assets, err := lifecycle.RuntimeAssets(toolset)
	if err != nil {
		return err
	}
	relayRegistry, err := lifecycle.SocketRelays(toolset)
	if err != nil {
		return err
	}

	runtimeRoot, err := openNativeRuntimeRoot(
		directories,
		uid,
		gid,
		r.logger,
	)
	if err != nil {
		return err
	}
	defer func() {
		r.logCleanup("close run runtime root", runtimeRoot.Close())
	}()

	runtimePaths, err := r.paths.ResolveRuntime()
	if err != nil {
		return err
	}
	hostRuntimeDirectory, err := safefs.OpenOrCreateRoot(
		runtimePaths.Root,
		safefs.DirectoryOptions{
			OwnerUID: uid,
			OwnerGID: gid,
			Logger:   r.logger,
		},
	)
	if err != nil {
		return fmt.Errorf("open Toby runtime root: %w", err)
	}
	defer func() {
		r.logCleanup(
			"close host runtime directory",
			hostRuntimeDirectory.Close(),
		)
	}()
	hostRuntimeRoot, err := hostRuntimeDirectory.File()
	if err != nil {
		return fmt.Errorf("duplicate Toby runtime root: %w", err)
	}
	defer func() {
		r.logCleanup("close host runtime root", hostRuntimeRoot.Close())
	}()

	socketRelays, err := relayRegistry.Start(
		ctx,
		runtimeRoot,
		r.logger,
	)
	if err != nil {
		return err
	}
	defer func() {
		if socketRelays != nil {
			r.logCleanup("close socket relays", socketRelays.Close())
		}
	}()

	tobyPath, tobyBinary, err := openSandboxBinary()
	if err != nil {
		return err
	}
	defer func() {
		r.logCleanup("close sandbox helper", tobyBinary.Close())
	}()

	assemblyOperation := r.status.StartOperation("Assembling sandbox")
	nativeRun, err := NewNativeRun(ctx, NativeRunInput{
		Prepared:    prepared,
		Home:        home,
		Managed:     nativeManagedInterfaces(managed),
		Directories: directories,
		Projects:    projects,
		Binds:       binds,
		ProtectedRoots: bwrap.ProtectedRoots{
			ImageStore:     imageStoreRoot,
			PersistentData: persistentDataRoot,
			RunStorage:     runStorageRoot,
			Runtime:        hostRuntimeRoot,
		},
		RuntimeRoot:       runtimeRoot,
		RuntimeAssets:     assets,
		SandboxGateway:    resources.Sandbox,
		SocketRelays:      socketRelays,
		ToolFiles:         files,
		Logger:            r.logger,
		SandboxBinaryPath: tobyPath,
		SandboxBinary:     tobyBinary,
		Workdir:           workdir,
		Identity:          identity,
		ToolSandbox:       toolSandbox,
		Executor:          executor,
	})
	if err != nil {
		return err
	}
	assemblyOperation.Finish(nil)
	if err := r.shutdown.Checkpoint(); err != nil {
		return err
	}
	prepared = nil
	home = nil
	managed = nil
	directories = nil
	socketRelays = nil
	defer func() {
		r.logCleanup("close native sandbox run", nativeRun.Close())
	}()

	if err := r.lifecycle.Initialize(
		ctx,
		toolset,
		lctx,
		options.Upgrade,
	); err != nil {
		return err
	}
	if err := r.shutdown.Checkpoint(); err != nil {
		return err
	}
	if options.Install {
		return nil
	}

	if err := r.status.Handoff(); err != nil {
		return err
	}
	launchErr := r.lifecycle.Launch(ctx, toolset, extra)

	return launchErr
}

func (r *NativeRunner) followAgentShutdown(
	ctx context.Context,
	session *agentclient.AgentSession,
) {
	select {
	case notice := <-session.Stopping():
		r.shutdown.RequestAgentStop(notice.GracePeriod)
	case <-session.Done():
	case <-ctx.Done():
	}
}

func (r *NativeRunner) startOperation(
	label string,
) (*status.Operation, io.Writer, io.Writer) {
	operation := r.status.StartOperation(label)
	return operation,
		operation.Writer(r.stdout),
		operation.Writer(r.stderr)
}

func (r *NativeRunner) logCleanup(message string, err error) {
	r.logger.DebugError(message, err)
}

func (r *NativeRunner) startLifecycleOperation(
	ctx context.Context,
	argv []string,
	options sandbox.ExecOptions,
) (bwrap.ProcessIO, func(error)) {
	if options.HideOutput && !r.status.RevealsHiddenOutput() {
		return bwrap.ProcessIO{Stdin: r.stdin}, nil
	}

	label := lifecycleOperationLabel(options)

	parent := lifecycle.CommandOperation(ctx)
	var operation *status.Operation
	if parent != nil {
		operation = parent.StartChild(label)
	}
	if operation == nil {
		operation = r.status.StartOperation(label)
	}
	stdout := operation.Writer(r.stdout)
	stderr := operation.Writer(r.stderr)

	return bwrap.ProcessIO{
		Stdin:  r.stdin,
		Stdout: stdout,
		Stderr: stderr,
		NotifyFinalizing: func() {
			operation.SetLabel("Finalizing")
		},
	}, operation.Finish
}

func lifecycleOperationLabel(options sandbox.ExecOptions) string {
	label := strings.TrimSpace(options.Status)
	if label == "" {
		return "Working"
	}
	return label
}

func (r *NativeRunner) validate() error {
	switch {
	case r == nil:
		return fmt.Errorf("native runner is nil")
	case r.registry == nil:
		return fmt.Errorf("native runner tool registry is not configured")
	case r.baseConfig == nil:
		return fmt.Errorf("native runner base configuration is not configured")
	case r.launchConfig == nil:
		return fmt.Errorf("native runner launch configuration is not configured")
	case r.sessionConfig == nil:
		return fmt.Errorf("native runner session configuration is not configured")
	case r.agent == nil:
		return fmt.Errorf("native runner agent client is not configured")
	case r.lifecycle == nil:
		return fmt.Errorf("native runner lifecycle is not configured")
	case r.sandbox == nil:
		return fmt.Errorf("native runner sandbox facade is not configured")
	case r.git == nil:
		return fmt.Errorf("native runner Git capability is not configured")
	case r.approval == nil:
		return fmt.Errorf("native runner approval service is not configured")
	case r.status == nil:
		return fmt.Errorf("native runner status service is not configured")
	case r.shutdown == nil:
		return fmt.Errorf("native runner shutdown service is not configured")
	case r.stdin == nil:
		return fmt.Errorf("native runner stdin is not configured")
	case r.stdout == nil:
		return fmt.Errorf("native runner stdout is not configured")
	case r.stderr == nil:
		return fmt.Errorf("native runner stderr is not configured")
	default:
		return nil
	}
}
