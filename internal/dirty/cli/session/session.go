package session

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"petris.dev/toby/config"
	"petris.dev/toby/config/toby"
	"petris.dev/toby/container/layout"
	"petris.dev/toby/context/files"
	"petris.dev/toby/control"
	"petris.dev/toby/control/host"
	"petris.dev/toby/control/methods/git"
	"petris.dev/toby/diagnostic/exitcode"
	"petris.dev/toby/diagnostic/warning"
	"petris.dev/toby/internal/dirty/control/mcpproxy"
	"petris.dev/toby/internal/dirty/control/mcpserver"
	"petris.dev/toby/internal/dirty/sandbox"
	"petris.dev/toby/lifecycle"
	"petris.dev/toby/platform/environ"
	"petris.dev/toby/sandbox/binary"
	"petris.dev/toby/tools"
)

type Params struct {
	Registry       *tools.Registry
	SandboxFactory sandbox.Factory
	SandboxService *sandbox.SandboxService
	Paths          config.Paths
	ContextFiles   *contextfiles.Service
	HostManager    *host.Service
	Git            *git.Service
	MCPProxy       *mcpproxy.Service
	MCPServer      *mcpserver.Runner
	TobyConfig     *tobyconfig.Service
	Stderr         io.Writer

	Runner *lifecycle.Runner
}

type Runner interface {
	Run(context.Context, *tools.Options, []string, []string, string) error
}

type RunnerFunc func(context.Context, *tools.Options, []string, []string, string) error

func (f RunnerFunc) Run(ctx context.Context, opts *tools.Options, extra, requestedTools []string, primary string) error {
	return f(ctx, opts, extra, requestedTools, primary)
}

func Run(ctx context.Context, params Params, opts *tools.Options, extra, requestedTools []string, primary string) error {
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
	lctx := lifecycle.Context{Options: opts, Stderr: params.Stderr}
	activeTools := toolset.OrderedToolNames()
	if err := params.Runner.RunPhase(ctx, lifecycle.PhaseHostPrepare, toolset, lctx, false); err != nil {
		return err
	}
	if params.ContextFiles == nil {
		return fmt.Errorf("context files service is not configured")
	}
	env := environ.Environment{"HOME": sbx.HomeDir()}
	params.ContextFiles.SetSandbox(params.SandboxService)
	params.ContextFiles.Reset()

	if params.HostManager == nil {
		return fmt.Errorf("host manager is not configured")
	}
	manager := params.HostManager
	if params.Git == nil {
		return fmt.Errorf("git capability is not configured")
	}
	params.Git.SetResolver(params.SandboxService)
	endpoint := sbx.HostControlEndpoint()
	routes := []control.Route{
		{Pattern: "/control", Auth: true, Handler: control.WebSocketHandler(manager.HandleConnection)},
		{Pattern: "/binary", Auth: true, Handler: binaryRoute(sandboxbinary.SourceBytes)},
	}
	if manager.HTTPProxy != nil {
		routes = append(routes, control.Route{Pattern: "/proxy/*", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			manager.HTTPProxy.HandleHTTP(r.Context(), w, r)
		})})
	}
	server, err := control.ListenEndpoint(ctx, endpoint, routes...)
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
	if err := runMountInit(ctx, params, manager, sbx, runSpec); err != nil {
		return err
	}
	sandboxClient, sandboxManagerExit, err := startRunSandboxManager(ctx, params, manager, sbx, opts, runSpec, toolset, lctx)
	if err != nil {
		return err
	}

	var runErr error
	if err := params.Runner.RunPhase(ctx, lifecycle.PhaseInitSandbox, toolset, lctx, false); err != nil {
		runErr = err
	} else {
		runErr = launchTool(ctx, params, toolset, opts, extra, lctx)
	}
	if termErr := terminateSandboxManager(ctx, sandboxClient, sandboxManagerExit); runErr == nil && termErr != nil {
		return termErr
	}
	return runErr
}

// binaryRoute serves the Toby binary bytes for the sandbox to download. Auth is
// enforced by the control server's token middleware (registered with Auth: true).
func binaryRoute(source func() ([]byte, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := source()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		_, _ = w.Write(data)
	})
}

func mcpDefaults(opts *tools.Options, config *tobyconfig.Service) mcpproxy.Defaults {
	var defaults mcpproxy.Defaults
	if config != nil {
		defaults.Image = config.MCPImage()
		defaults.EffectiveImage = config.Container().Image
	}
	if opts != nil && strings.TrimSpace(opts.Image) != "" {
		defaults.EffectiveImage = strings.TrimSpace(opts.Image)
	}
	if opts != nil {
		defaults.Debug = opts.DebugEnabled()
	}
	return defaults
}

func ApplySandboxDefaults(opts *tools.Options, config *tobyconfig.Service) tools.Options {
	if opts == nil {
		opts = &tools.Options{}
	}
	result := *opts
	container := config.Container()
	settings := config.Settings()
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
	// Container image/build defaults apply to the docker runtime (the default).
	// A non-docker runtime selected via --runtime gets no container defaults.
	if result.SandboxRuntime != "" && result.SandboxRuntime != sandbox.RuntimeDocker {
		return result
	}
	if result.Image == "" {
		result.Image = container.Image
	}
	if !result.Build.IsSet() {
		result.Build = container.Build
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

func prepareConfiguredProjects(stderr io.Writer, home string, opts *tools.Options) error {
	if opts == nil || len(opts.Projects) == 0 {
		return nil
	}
	projects := make([]tools.ProjectMount, 0, len(opts.Projects))
	seen := map[string]tools.ProjectMount{}
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

func resolveConfiguredProjectSource(project tools.ProjectMount, home string) (tools.ProjectMount, bool, error) {
	name := strings.TrimSpace(project.Name)
	source := strings.TrimSpace(project.Source)
	if source == "" {
		return tools.ProjectMount{}, false, exitcode.New(2, "configured project %s source is required", name)
	}
	abs, err := filepath.Abs(config.ExpandHome(source, home))
	if err != nil {
		return tools.ProjectMount{}, false, err
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return tools.ProjectMount{Name: name, Source: abs}, false, nil
	}
	return tools.ProjectMount{Name: name, Source: abs}, true, nil
}

func sandboxManagerArgv(sbx sandbox.Instance) []string {
	return []string{
		"/bin/sh", "-c",
		`set -e; mkdir -p "$1"; curl -fsSL -H "Authorization: Bearer ${TOBY_CONTROL_TOKEN:?}" "http://${TOBY_CONTROL_HOST:?}/binary" -o "$2"; chmod 755 "$2"; exec "$2" sandbox manager`,
		"toby-startup", sbx.TobyBinDir(), sbx.TobyBinaryPath(),
	}
}

func runMountInit(ctx context.Context, params Params, manager *host.Service, sbx sandbox.Instance, spec sandbox.RunSpec) error {
	exits := sandbox.NewCommandExits()
	ready := make(chan sandboxManagerReady, 1)
	managerExit := sandbox.NewManagerExit()
	manager.CommandExit = exits.Complete
	manager.ContextInit = func(ctx context.Context, client *host.SandboxClient) error {
		if err := params.SandboxService.Connect(ctx, sbx, client, exits, managerExit); err != nil {
			return err
		}
		return params.SandboxService.MountSetup(ctx)
	}
	manager.SandboxReady = func(client *host.SandboxClient, err error) {
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

func startRunSandboxManager(ctx context.Context, params Params, manager *host.Service, sbx sandbox.Instance, opts *tools.Options, spec sandbox.RunSpec, toolset *tools.Toolset, lctx lifecycle.Context) (*host.SandboxClient, *sandbox.ManagerExit, error) {
	exits := sandbox.NewCommandExits()
	ready := make(chan sandboxManagerReady, 1)
	managerExit := sandbox.NewManagerExit()
	manager.CommandExit = exits.Complete
	manager.ContextInit = func(ctx context.Context, client *host.SandboxClient) error {
		if err := params.SandboxService.Connect(ctx, sbx, client, exits, managerExit); err != nil {
			return err
		}
		if err := params.Runner.RunPhase(ctx, lifecycle.PhaseConfigureSandbox, toolset, lctx, false); err != nil {
			return err
		}
		return initSandboxContext(ctx, params, toolset, lctx)
	}
	manager.SandboxReady = func(client *host.SandboxClient, err error) {
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
	client *host.SandboxClient
	err    error
}

func initSandboxContext(ctx context.Context, params Params, toolset *tools.Toolset, lctx lifecycle.Context) error {
	if params.ContextFiles == nil {
		return fmt.Errorf("context files service is not configured")
	}
	contextDir := layout.Context
	if err := params.SandboxService.DeletePath(ctx, contextDir, true); err != nil {
		return err
	}
	return params.Runner.RunPhase(ctx, lifecycle.PhaseContextFiles, toolset, lctx, false)
}

func launchTool(ctx context.Context, params Params, toolset *tools.Toolset, opts *tools.Options, extra []string, lctx lifecycle.Context) error {
	primary := toolset.Primary()
	if primary == nil {
		return fmt.Errorf("toolset cannot launch without a primary tool")
	}
	if opts != nil && opts.Install {
		return params.Runner.RunPhase(ctx, lifecycle.PhaseInstall, toolset, lctx, false)
	}
	if opts != nil && opts.Upgrade {
		if err := params.Runner.RunPhase(ctx, lifecycle.PhaseInstall, toolset, lctx, true); err != nil {
			return err
		}
		return primary.Launch(ctx, extra)
	}
	if err := params.Runner.RunPhase(ctx, lifecycle.PhaseInstall, toolset, lctx, false); err != nil {
		return err
	}
	return primary.Launch(ctx, extra)
}

func registerTobyMCPProxy(params Params, manager *host.Service, controlHost string, opts *tools.Options, activeTools []string, primary string) (string, error) {
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
	id, err := manager.HTTPProxy.RegisterHandler(params.MCPServer.Handler(mcpserver.NewHostManagerGitClient(manager), state))
	if err != nil {
		return "", err
	}
	return control.Endpoint{Host: controlHost}.ProxyBaseURL(id), nil
}

func waitSandboxManagerAfterError(ctx context.Context, exit *sandbox.ManagerExit, client *host.SandboxClient, err error) error {
	_ = terminateSandboxManager(ctx, client, exit)
	return err
}

func terminateSandboxManager(ctx context.Context, client *host.SandboxClient, exit *sandbox.ManagerExit) error {
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
