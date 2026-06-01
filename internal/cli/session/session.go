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
	"petris.dev/toby/internal/context/setup"
	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/control/hostmanager"
	"petris.dev/toby/internal/control/httpproxy"
	"petris.dev/toby/internal/control/mcpserver"
	"petris.dev/toby/internal/diagnostic/exitcode"
	"petris.dev/toby/internal/diagnostic/warning"
	"petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/sandbox/binary"
	"petris.dev/toby/internal/tools/helpers"
	"petris.dev/toby/internal/tools/tool"
)

type Params struct {
	Registry       *tool.Registry
	SandboxFactory sandbox.Factory
	SandboxService *sandbox.SandboxService
	Paths          config.Paths
	ContextFiles   *contextfiles.Service
	ContextInit    []contextinit.Registration
	HostManager    *hostmanager.HostManager
	MCPServer      *mcpserver.Runner
	TobyConfig     *tobyconfig.Service
	Stderr         io.Writer
}

func Run(ctx context.Context, params Params, opts *tool.CommandOptions, extra, requestedTools []string, primary string) error {
	effectiveOpts := applySandboxDefaults(opts, params.TobyConfig)
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

	toolset, err := params.Registry.Build(requestedTools, primary)
	if err != nil {
		return err
	}
	toolset.SetToolStates(opts.ToolStates)
	warnHostToolState(params.Stderr, opts.SuppressWarnings, toolset)
	if err := toolset.HostInit(ctx, opts); err != nil {
		return err
	}
	if params.ContextFiles == nil {
		return fmt.Errorf("context files service is not configured")
	}
	env := helpers.EnvironmentFromList(os.Environ())
	params.ContextFiles.SetSandbox(params.SandboxService)
	params.ContextFiles.Reset()
	sbx.SetupEnvironment(env)

	exits := sandbox.NewCommandExits()
	ready := make(chan sandboxManagerReady, 1)
	if params.HostManager == nil {
		return fmt.Errorf("host manager is not configured")
	}
	manager := params.HostManager
	manager.RepositoryResolver = params.SandboxService
	manager.CommandExit = exits.Complete
	sandboxManagerExit := sandbox.NewManagerExit()
	manager.ContextInit = func(ctx context.Context, client *hostmanager.SandboxClient) error {
		if err := params.SandboxService.Connect(ctx, sbx, client, exits, sandboxManagerExit); err != nil {
			return err
		}
		if err := toolset.SandboxContextSetup(ctx); err != nil {
			return err
		}
		return initSandboxContext(ctx, params, toolset, opts)
	}
	manager.SandboxReady = func(client *hostmanager.SandboxClient, err error) {
		ready <- sandboxManagerReady{client: client, err: err}
	}
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
	mcpURL, err := registerTobyMCPProxy(params, manager, env[control.EnvControlHost])
	if err != nil {
		return err
	}
	params.SandboxService.SetTobyMCPURL(mcpURL)

	binds := params.SandboxService.StartBinds()
	go func() {
		code, err := sbx.Run(ctx, sandbox.RunSpec{Argv: sandboxManagerArgv(sbx), Env: env, Binds: binds})
		sandboxManagerExit.Set(sandbox.ProcessResult{ExitCode: code, Err: err})
	}()

	var sandboxClient *hostmanager.SandboxClient
	select {
	case result := <-ready:
		if result.err != nil {
			return waitSandboxManagerAfterError(ctx, sandboxManagerExit, result.client, result.err)
		}
		sandboxClient = result.client
	case <-sandboxManagerExit.Done():
		result := sandboxManagerExit.Result()
		if result.Err != nil {
			return result.Err
		}
		return exitcode.New(result.ExitCode, "sandbox manager exited before context init")
	case <-ctx.Done():
		return ctx.Err()
	}
	var runErr error
	if err := toolset.SandboxInit(ctx); err != nil {
		runErr = err
	} else {
		runErr = toolset.Launch(ctx, opts, extra)
	}
	if termErr := terminateSandboxManager(ctx, sandboxClient, sandboxManagerExit); runErr == nil && termErr != nil {
		return termErr
	}
	return runErr
}

func applySandboxDefaults(opts *tool.CommandOptions, config *tobyconfig.Service) tool.CommandOptions {
	if opts == nil {
		opts = &tool.CommandOptions{}
	}
	result := *opts
	defaults := config.Sandbox()
	result.ToolStates = defaults.Tools.Clone()
	result.ToolStates.Merge(opts.ToolStates)
	result.SuppressWarnings = defaults.SuppressWarnings.Clone()
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

func warnHostToolState(stderr io.Writer, suppression warning.Suppression, toolset *tool.Toolset) {
	names := toolset.HostStateToolNames()
	if len(names) == 0 {
		return
	}
	warning.Fprintf(stderr, suppression, warning.ToolHostState, "using host tool state for %s; running multiple sandbox instances with the same host tool state can corrupt tool databases.", strings.Join(names, ", "))
}

func sandboxManagerArgv(sbx sandbox.Instance) []string {
	return []string{
		"/bin/sh", "-c",
		`set -e; mkdir -p "${TOBY_BIN_DIR:?}"; curl -fsSL -H "Authorization: Bearer ${TOBY_CONTROL_TOKEN}" "http://${TOBY_CONTROL_HOST}/binary" -o "${TOBY_BIN_DIR}/toby"; chmod 755 "${TOBY_BIN_DIR}/toby"; exec "$@"`,
		"toby-startup", sbx.TobyBinaryPath(), "sandbox", "manager",
	}
}

type sandboxManagerReady struct {
	client *hostmanager.SandboxClient
	err    error
}

func initSandboxContext(ctx context.Context, params Params, toolset *tool.Toolset, opts *tool.CommandOptions) error {
	if params.ContextFiles == nil {
		return fmt.Errorf("context files service is not configured")
	}
	contextDir := params.SandboxService.Paths().Context
	if err := params.SandboxService.DeletePath(ctx, contextDir, true); err != nil {
		return err
	}
	for _, service := range contextinit.Ordered(params.ContextInit) {
		if err := service.InitContext(ctx, contextinit.Params{Toolset: toolset, Options: opts, Stderr: params.Stderr}); err != nil {
			return err
		}
	}
	return nil
}

func registerTobyMCPProxy(params Params, manager *hostmanager.HostManager, controlHost string) (string, error) {
	if params.MCPServer == nil {
		return "", fmt.Errorf("mcp server runner is not configured")
	}
	if manager == nil || manager.HTTPProxy == nil {
		return "", fmt.Errorf("http proxy service is not configured")
	}
	if strings.TrimSpace(controlHost) == "" {
		return "", fmt.Errorf("%s is required", control.EnvControlHost)
	}
	id, err := manager.HTTPProxy.Register(httpproxy.Target{Handler: params.MCPServer.Handler(mcpserver.NewHostManagerGitClient(manager))})
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
