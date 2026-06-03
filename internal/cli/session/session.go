package session

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/config/toby"
	"petris.dev/toby/internal/context/files"
	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/control/hostmanager"
	"petris.dev/toby/internal/control/httpproxy"
	"petris.dev/toby/internal/control/mcpproxy"
	"petris.dev/toby/internal/control/mcpserver"
	"petris.dev/toby/internal/diagnostic/exitcode"
	"petris.dev/toby/internal/diagnostic/warning"
	"petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/sandbox/binary"
	sandboxmount "petris.dev/toby/internal/sandbox/mount"
	"petris.dev/toby/internal/tools/tool"
)

type Params struct {
	Registry       *tool.Registry
	SandboxFactory sandbox.Factory
	SandboxService *sandbox.SandboxService
	Paths          config.Paths
	ContextFiles   *contextfiles.Service
	HostManager    *hostmanager.HostManager
	MCPProxy       *mcpproxy.Service
	MCPServer      *mcpserver.Runner
	TobyConfig     *tobyconfig.Service
	Stderr         io.Writer

	HostInitHooks     []tool.LifecycleHook
	MountInitHooks    []tool.LifecycleHook
	ContextSetupHooks []tool.LifecycleHook
	ContextInitHooks  []tool.LifecycleHook
	SandboxInitHooks  []tool.LifecycleHook
	InstallHooks      []tool.LifecycleHook
	UpgradeHooks      []tool.LifecycleHook
}

type Runner interface {
	Run(context.Context, *tool.CommandOptions, []string, []string, string) error
}

type RunnerFunc func(context.Context, *tool.CommandOptions, []string, []string, string) error

func (f RunnerFunc) Run(ctx context.Context, opts *tool.CommandOptions, extra, requestedTools []string, primary string) error {
	return f(ctx, opts, extra, requestedTools, primary)
}

func Run(ctx context.Context, params Params, opts *tool.CommandOptions, extra, requestedTools []string, primary string) error {
	effectiveOpts := ApplySandboxDefaults(opts, params.TobyConfig)
	opts = &effectiveOpts
	if err := prepareConfiguredProjects(params.Stderr, params.Paths.Home, opts); err != nil {
		return err
	}
	sbx, err := params.SandboxFactory.FromOptions(opts)
	if err != nil {
		return err
	}
	defer sbx.Cleanup()
	if params.SandboxService == nil {
		return fmt.Errorf("sandbox service is not configured")
	}
	params.SandboxService.Prepare(sbx)
	if err := params.SandboxService.ConfigureMounts(opts); err != nil {
		return err
	}

	toolset, err := params.Registry.Build(requestedTools, primary)
	if err != nil {
		return err
	}
	lifecycleCtx := tool.LifecycleContext{Options: opts, Stderr: params.Stderr}
	activeTools := toolset.OrderedToolNames()
	if err := tool.RunLifecycle(ctx, params.HostInitHooks, activeTools, lifecycleCtx); err != nil {
		return err
	}
	warnHostBackedMounts(params.Stderr, opts.SuppressWarnings, params.SandboxService.HostBackedManagedMounts())
	if params.ContextFiles == nil {
		return fmt.Errorf("context files service is not configured")
	}
	env := tool.Environment{"HOME": sbx.HomeDir()}
	params.ContextFiles.SetSandbox(params.SandboxService)
	params.ContextFiles.Reset()

	if params.HostManager == nil {
		return fmt.Errorf("host manager is not configured")
	}
	manager := params.HostManager
	manager.RepositoryResolver = params.SandboxService
	endpoint := sbx.HostControlEndpoint()
	endpoint.BinarySource = sandboxbinary.SourceBytes
	routes := []control.HTTPRoute{}
	if manager.HTTPProxy != nil {
		routes = append(routes, control.HTTPRoute{Pattern: "/proxy/", Handler: func(ctx context.Context, w http.ResponseWriter, r *http.Request) {
			manager.HTTPProxy.HandleHTTP(ctx, w, r)
		}})
	}
	server, err := control.ListenEndpoint(ctx, endpoint, manager.HandleConnection, routes...)
	if err != nil {
		return err
	}
	defer server.Close()
	sbx.SetupControlEndpoint(env, server.Endpoint)
	mcpURL, err := registerTobyMCPProxy(params, manager, env[control.EnvControlHost], opts, activeTools, primary)
	if err != nil {
		return err
	}
	params.SandboxService.SetTobyMCPURL(mcpURL)
	if params.MCPProxy != nil {
		if err := params.MCPProxy.Configure(ctx, env[control.EnvControlHost], params.TobyConfig, mcpDefaults(opts, params.TobyConfig)); err != nil {
			return err
		}
		params.MCPProxy.StartAll(ctx)
		defer params.MCPProxy.StopAll(context.Background())
	}

	mounts := params.SandboxService.RuntimeMounts()
	binds := params.SandboxService.StartBinds()
	runSpec := sandbox.RunSpec{Argv: sandboxManagerArgv(sbx), Env: env, Binds: binds, Mounts: mounts, Debug: opts.DebugEnabled()}
	primeCode, primeErr := sbx.Prime(ctx, runSpec)
	if primeErr != nil {
		return primeErr
	}
	if primeCode != 0 {
		return exitcode.Code(primeCode)
	}
	if err := runMountInit(ctx, params, manager, sbx, runSpec, activeTools, lifecycleCtx); err != nil {
		return err
	}
	sandboxClient, sandboxManagerExit, err := startRunSandboxManager(ctx, params, manager, sbx, opts, runSpec, activeTools, lifecycleCtx)
	if err != nil {
		return err
	}

	var runErr error
	if err := tool.RunLifecycle(ctx, params.SandboxInitHooks, activeTools, lifecycleCtx); err != nil {
		runErr = err
	} else {
		runErr = launchTool(ctx, params, toolset, opts, extra, activeTools, lifecycleCtx)
	}
	if termErr := terminateSandboxManager(ctx, sandboxClient, sandboxManagerExit); runErr == nil && termErr != nil {
		return termErr
	}
	return runErr
}

func mcpDefaults(opts *tool.CommandOptions, config *tobyconfig.Service) mcpproxy.Defaults {
	var defaults mcpproxy.Defaults
	if config != nil {
		defaults.Runtime = config.MCPSandbox().Runtime
		sandboxDefaults := config.Sandbox()
		defaults.EffectiveDockerImage = sandboxDefaults.Runtime.Docker.Image
	}
	if opts != nil && strings.TrimSpace(opts.DockerImage) != "" {
		defaults.EffectiveDockerImage = strings.TrimSpace(opts.DockerImage)
	}
	if opts != nil {
		defaults.Debug = opts.DebugEnabled()
	}
	return defaults
}

func ApplySandboxDefaults(opts *tool.CommandOptions, config *tobyconfig.Service) tool.CommandOptions {
	if opts == nil {
		opts = &tool.CommandOptions{}
	}
	result := *opts
	defaults := config.Sandbox()
	settings := config.Settings()
	result.MountProfiles = config.MountProfiles()
	result.MountProfiles.Merge(opts.MountProfiles)
	if result.MountProfile == "" {
		result.MountProfile = settings.MountProfile
	}
	if result.Debug == nil && settings.Debug != nil {
		debug := *settings.Debug
		result.Debug = &debug
	}
	if result.Yolo == nil && settings.Yolo != nil {
		yolo := *settings.Yolo
		result.Yolo = &yolo
	}
	result.ToolMountProfiles = config.ToolMountProfiles()
	mergeStringMap(result.ToolMountProfiles, opts.ToolMountProfiles)
	result.SuppressWarnings = settings.SuppressWarnings.Clone()
	result.SuppressWarnings.Merge(opts.SuppressWarnings)
	if result.BubblewrapRoot == "" {
		result.BubblewrapRoot = defaults.Runtime.Bubblewrap.Root
	}
	if result.SandboxRuntime == "" {
		result.SandboxRuntime = defaults.Runtime.Default
	}
	if result.SandboxRuntime != "docker" {
		return result
	}
	if result.DockerImage == "" {
		result.DockerImage = defaults.Runtime.Docker.Image
	}
	if result.DockerHome == "" {
		result.DockerHome = defaults.Runtime.Docker.Home
	}
	if result.DockerProjects == "" {
		result.DockerProjects = defaults.Runtime.Docker.Projects
	}
	if !result.DockerBuild.IsSet() {
		result.DockerBuild = defaults.Runtime.Docker.Build
	}
	return result
}

func cloneStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	clone := make(map[string]string, len(src))
	for key, value := range src {
		clone[key] = value
	}
	return clone
}

func mergeStringMap(dst, src map[string]string) {
	if len(src) == 0 {
		return
	}
	for key, value := range src {
		dst[key] = value
	}
}

func prepareConfiguredProjects(stderr io.Writer, home string, opts *tool.CommandOptions) error {
	if opts == nil || len(opts.Projects) == 0 {
		return nil
	}
	projects := make([]tool.ProjectMount, 0, len(opts.Projects))
	seen := map[string]tool.ProjectMount{}
	for _, project := range opts.Projects {
		resolved, exists, err := resolveConfiguredProjectSource(project, home)
		if err != nil {
			return err
		}
		if !exists {
			warning.Fprintf(stderr, opts.SuppressWarnings, warning.ProjectMissing, "configured project %q does not exist: %s; skipping it.", resolved.Name, resolved.Source)
			continue
		}
		if previous, ok := seen[resolved.Name]; ok {
			warning.Fprintf(stderr, opts.SuppressWarnings, warning.ProjectDuplicate, "configured project %q duplicates an earlier project name; using %s and skipping %s.", resolved.Name, previous.Source, resolved.Source)
			continue
		}
		seen[resolved.Name] = resolved
		projects = append(projects, resolved)
	}
	if len(projects) == 0 {
		return exitcode.New(1, "launch config projects must include at least one existing project")
	}
	if opts.Env == "" {
		opts.Env = projects[0].Name
	}
	opts.Projects = projects
	return nil
}

func resolveConfiguredProjectSource(project tool.ProjectMount, home string) (tool.ProjectMount, bool, error) {
	name := strings.TrimSpace(project.Name)
	source := strings.TrimSpace(project.Source)
	if source == "" {
		return tool.ProjectMount{}, false, exitcode.New(2, "configured project %s source is required", name)
	}
	abs, err := filepath.Abs(config.ExpandHome(source, home))
	if err != nil {
		return tool.ProjectMount{}, false, err
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return tool.ProjectMount{Name: name, Source: abs}, false, nil
	}
	return tool.ProjectMount{Name: name, Source: abs}, true, nil
}

func warnHostBackedMounts(stderr io.Writer, suppression warning.Suppression, mounts []sandboxmount.Info) {
	if len(mounts) == 0 {
		return
	}
	names := make([]string, 0, len(mounts))
	for _, item := range mounts {
		names = append(names, item.Key.String())
	}
	warning.Fprintf(stderr, suppression, warning.MountHostBacking, "using host-backed managed mounts for %s; running multiple sandbox instances with the same host-backed mounts can corrupt tool databases.", strings.Join(names, ", "))
}

func sandboxManagerArgv(sbx sandbox.Instance) []string {
	return []string{
		"/bin/sh", "-c",
		`set -e; mkdir -p "$1"; curl -fsSL -H "Authorization: Bearer ${TOBY_CONTROL_TOKEN:?}" "http://${TOBY_CONTROL_HOST:?}/binary" -o "$2"; chmod 755 "$2"; exec "$2" sandbox manager`,
		"toby-startup", sbx.TobyBinDir(), sbx.TobyBinaryPath(),
	}
}

func runMountInit(ctx context.Context, params Params, manager *hostmanager.HostManager, sbx sandbox.Instance, spec sandbox.RunSpec, activeTools []string, lifecycleCtx tool.LifecycleContext) error {
	exits := sandbox.NewCommandExits()
	ready := make(chan sandboxManagerReady, 1)
	managerExit := sandbox.NewManagerExit()
	manager.CommandExit = exits.Complete
	manager.ContextInit = func(ctx context.Context, client *hostmanager.SandboxClient) error {
		if err := params.SandboxService.Connect(ctx, sbx, client, exits, managerExit); err != nil {
			return err
		}
		if err := params.SandboxService.MountSetup(ctx); err != nil {
			return err
		}
		return tool.RunLifecycle(ctx, params.MountInitHooks, activeTools, lifecycleCtx)
	}
	manager.SandboxReady = func(client *hostmanager.SandboxClient, err error) {
		ready <- sandboxManagerReady{client: client, err: err}
	}
	go func() {
		code, err := sbx.Setup(ctx, spec)
		managerExit.Set(sandbox.ProcessResult{ExitCode: code, Err: err})
	}()
	select {
	case result := <-ready:
		if result.err != nil {
			return waitSandboxManagerAfterError(ctx, managerExit, result.client, result.err)
		}
		return terminateSandboxManager(ctx, result.client, managerExit)
	case <-managerExit.Done():
		result := managerExit.Result()
		if result.Err != nil {
			return result.Err
		}
		return exitcode.New(result.ExitCode, "sandbox setup manager exited before context init")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func startRunSandboxManager(ctx context.Context, params Params, manager *hostmanager.HostManager, sbx sandbox.Instance, opts *tool.CommandOptions, spec sandbox.RunSpec, activeTools []string, lifecycleCtx tool.LifecycleContext) (*hostmanager.SandboxClient, *sandbox.ManagerExit, error) {
	exits := sandbox.NewCommandExits()
	ready := make(chan sandboxManagerReady, 1)
	managerExit := sandbox.NewManagerExit()
	manager.CommandExit = exits.Complete
	manager.ContextInit = func(ctx context.Context, client *hostmanager.SandboxClient) error {
		if err := params.SandboxService.Connect(ctx, sbx, client, exits, managerExit); err != nil {
			return err
		}
		if err := tool.RunLifecycle(ctx, params.ContextSetupHooks, activeTools, lifecycleCtx); err != nil {
			return err
		}
		return initSandboxContext(ctx, params, opts, activeTools, lifecycleCtx)
	}
	manager.SandboxReady = func(client *hostmanager.SandboxClient, err error) {
		ready <- sandboxManagerReady{client: client, err: err}
	}
	go func() {
		code, err := sbx.Run(ctx, spec)
		managerExit.Set(sandbox.ProcessResult{ExitCode: code, Err: err})
	}()
	select {
	case result := <-ready:
		if result.err != nil {
			return nil, nil, waitSandboxManagerAfterError(ctx, managerExit, result.client, result.err)
		}
		return result.client, managerExit, nil
	case <-managerExit.Done():
		result := managerExit.Result()
		if result.Err != nil {
			return nil, nil, result.Err
		}
		return nil, nil, exitcode.New(result.ExitCode, "sandbox manager exited before context init")
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
}

type sandboxManagerReady struct {
	client *hostmanager.SandboxClient
	err    error
}

func initSandboxContext(ctx context.Context, params Params, opts *tool.CommandOptions, activeTools []string, lifecycleCtx tool.LifecycleContext) error {
	if params.ContextFiles == nil {
		return fmt.Errorf("context files service is not configured")
	}
	contextDir := params.SandboxService.Paths().Context
	if err := params.SandboxService.DeletePath(ctx, contextDir, true); err != nil {
		return err
	}
	lifecycleCtx.Options = opts
	return tool.RunLifecycle(ctx, params.ContextInitHooks, activeTools, lifecycleCtx)
}

func launchTool(ctx context.Context, params Params, toolset *tool.Toolset, opts *tool.CommandOptions, extra []string, activeTools []string, lifecycleCtx tool.LifecycleContext) error {
	primary := toolset.Primary()
	if primary == nil {
		return fmt.Errorf("toolset cannot launch without a primary tool")
	}
	if opts != nil && opts.Install {
		return tool.RunLifecycle(ctx, params.InstallHooks, activeTools, lifecycleCtx)
	}
	if opts != nil && opts.Upgrade {
		if err := tool.RunLifecycle(ctx, params.UpgradeHooks, activeTools, lifecycleCtx); err != nil {
			return err
		}
		return primary.Launch(ctx, extra)
	}
	if err := tool.RunLifecycle(ctx, params.InstallHooks, activeTools, lifecycleCtx); err != nil {
		return err
	}
	return primary.Launch(ctx, extra)
}

func registerTobyMCPProxy(params Params, manager *hostmanager.HostManager, controlHost string, opts *tool.CommandOptions, activeTools []string, primary string) (string, error) {
	if params.MCPServer == nil {
		return "", fmt.Errorf("mcp server runner is not configured")
	}
	if manager == nil || manager.HTTPProxy == nil {
		return "", fmt.Errorf("http proxy service is not configured")
	}
	if strings.TrimSpace(controlHost) == "" {
		return "", fmt.Errorf("%s is required", control.EnvControlHost)
	}
	state := mcpserver.SessionState{Debug: opts != nil && opts.DebugEnabled(), Paths: params.Paths, Sandbox: params.SandboxService, MCPProxy: params.MCPProxy, Config: params.TobyConfig, Registry: params.Registry, ActiveTools: activeTools, PrimaryTool: primary}
	if opts != nil {
		state.Options = *opts
	}
	id, err := manager.HTTPProxy.Register(httpproxy.Target{Handler: params.MCPServer.Handler(mcpserver.NewHostManagerGitClient(manager), state)})
	if err != nil {
		return "", err
	}
	return control.Endpoint{Host: controlHost}.ProxyBaseURL(id), nil
}

func waitSandboxManagerAfterError(ctx context.Context, exit *sandbox.ManagerExit, client *hostmanager.SandboxClient, err error) error {
	_ = terminateSandboxManager(ctx, client, exit)
	return err
}

func terminateSandboxManager(ctx context.Context, client *hostmanager.SandboxClient, exit *sandbox.ManagerExit) error {
	if client != nil {
		if err := client.Terminate(ctx); err != nil {
			return err
		}
	}
	select {
	case <-exit.Done():
		result := exit.Result()
		if result.Err != nil {
			return result.Err
		}
		if result.ExitCode != 0 {
			return exitcode.Code(result.ExitCode)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
