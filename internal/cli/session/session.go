package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/contextfiles"
	"petris.dev/toby/internal/contextinit"
	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/exitcode"
	"petris.dev/toby/internal/hostmanager"
	"petris.dev/toby/internal/httpproxy"
	"petris.dev/toby/internal/mcpserver"
	"petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/sandboxbinary"
	"petris.dev/toby/internal/tobyconfig"
	"petris.dev/toby/internal/tool"
	"petris.dev/toby/internal/warning"
)

type Params struct {
	Registry       *tool.Registry
	SandboxFactory sandbox.Factory
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

	toolset, err := params.Registry.Build(requestedTools, primary)
	if err != nil {
		return err
	}
	toolset.SetToolStates(opts.ToolStates)
	warnHostToolState(params.Stderr, opts.SuppressWarnings, toolset)
	if err := toolset.HostInit(ctx, opts); err != nil {
		return err
	}
	env := tool.EnvironmentFromList(os.Environ())
	run := &tool.RunContext{
		Sandbox: sbx,
		Options: opts,
		Extra:   extra,
		Toolset: toolset,
		Env:     env,
		Stderr:  params.Stderr,
	}
	sbx.SetupContext(run)
	if err := toolset.SandboxContextSetup(run); err != nil {
		return err
	}

	exits := newCommandExits()
	ready := make(chan sandboxManagerReady, 1)
	if params.HostManager == nil {
		return fmt.Errorf("host manager is not configured")
	}
	manager := params.HostManager
	manager.RepositoryResolver = sbx
	manager.CommandExit = exits.complete
	manager.ContextInit = func(ctx context.Context, client *hostmanager.SandboxClient) error {
		return initSandboxContext(ctx, params, sbx, run, client)
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
	run.TobyMCPURL, err = registerTobyMCPProxy(params, manager, env[control.EnvControlHost])
	if err != nil {
		return err
	}

	sandboxManagerExit := newSandboxManagerExit()
	go func() {
		code, err := sbx.Run(ctx, sandbox.RunSpec{Argv: sandboxManagerArgv(sbx), Toolset: toolset, Env: env})
		sandboxManagerExit.set(sandboxManagerProcessResult{exitCode: code, err: err})
	}()

	var sandboxClient *hostmanager.SandboxClient
	select {
	case result := <-ready:
		if result.err != nil {
			return waitSandboxManagerAfterError(ctx, sandboxManagerExit, result.client, result.err)
		}
		sandboxClient = result.client
	case <-sandboxManagerExit.done:
		result := sandboxManagerExit.result()
		if result.err != nil {
			return result.err
		}
		return exitcode.New(result.exitCode, "sandbox manager exited before context init")
	case <-ctx.Done():
		return ctx.Err()
	}
	executor := &sandboxManagerExecutor{client: sandboxClient, exits: exits, sandboxManagerExit: sandboxManagerExit}
	run.Exec = func(ctx context.Context, argv []string, options tool.ExecOptions) (int, error) {
		return executor.run(ctx, argv, options, false)
	}
	run.Launch = func(ctx context.Context, argv []string, options tool.ExecOptions) (int, error) {
		return executor.run(ctx, argv, options, true)
	}
	var runErr error
	if err := toolset.SandboxInit(ctx, run); err != nil {
		runErr = err
	} else {
		runErr = toolset.Launch(ctx, run)
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
		`set -e; mkdir -p /tmp/toby/bin; curl -fsSL -H "Authorization: Bearer ${TOBY_CONTROL_TOKEN}" "http://${TOBY_CONTROL_HOST}/binary" -o /tmp/toby/bin/toby; chmod 755 /tmp/toby/bin/toby; exec "$@"`,
		"toby-startup", sbx.TobyBinaryPath(), "sandbox", "manager",
	}
}

type sandboxManagerReady struct {
	client *hostmanager.SandboxClient
	err    error
}

type sandboxManagerProcessResult struct {
	exitCode int
	err      error
}

type sandboxManagerExit struct {
	done chan struct{}
	once sync.Once
	mu   sync.Mutex
	res  sandboxManagerProcessResult
}

func newSandboxManagerExit() *sandboxManagerExit {
	return &sandboxManagerExit{done: make(chan struct{})}
}

func (s *sandboxManagerExit) set(result sandboxManagerProcessResult) {
	s.mu.Lock()
	s.res = result
	s.mu.Unlock()
	s.once.Do(func() { close(s.done) })
}

func (s *sandboxManagerExit) result() sandboxManagerProcessResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.res
}

type commandExits struct {
	mu      sync.Mutex
	waiting map[string]chan control.CommandExitParams
}

func newCommandExits() *commandExits {
	return &commandExits{waiting: map[string]chan control.CommandExitParams{}}
}

func (e *commandExits) watch(commandID string) chan control.CommandExitParams {
	ch := make(chan control.CommandExitParams, 1)
	e.mu.Lock()
	e.waiting[commandID] = ch
	e.mu.Unlock()
	return ch
}

func (e *commandExits) unwatch(commandID string) {
	e.mu.Lock()
	delete(e.waiting, commandID)
	e.mu.Unlock()
}

func (e *commandExits) complete(params control.CommandExitParams) {
	e.mu.Lock()
	ch := e.waiting[params.CommandID]
	delete(e.waiting, params.CommandID)
	e.mu.Unlock()
	if ch != nil {
		ch <- params
	}
}

type sandboxManagerExecutor struct {
	client             *hostmanager.SandboxClient
	exits              *commandExits
	sandboxManagerExit *sandboxManagerExit
}

func (e *sandboxManagerExecutor) run(ctx context.Context, argv []string, options tool.ExecOptions, foreground bool) (int, error) {
	commandID, err := control.NewCommandID()
	if err != nil {
		return 1, err
	}
	exitCh := e.exits.watch(commandID)
	if err := e.client.CommandRun(ctx, control.CommandRunParams{CommandID: commandID, Argv: argv, Foreground: foreground, HideOutput: options.HideOutput}); err != nil {
		e.exits.unwatch(commandID)
		return 1, err
	}
	select {
	case result := <-exitCh:
		if result.Error != "" {
			return result.ExitCode, errors.New(result.Error)
		}
		return result.ExitCode, nil
	case <-e.sandboxManagerExit.done:
		result := e.sandboxManagerExit.result()
		if result.err != nil {
			return 1, result.err
		}
		return result.exitCode, fmt.Errorf("sandbox manager exited before command completed")
	case <-ctx.Done():
		e.exits.unwatch(commandID)
		return 130, ctx.Err()
	}
}

type sandboxManagerContextSink struct {
	ctx    context.Context
	client *hostmanager.SandboxClient
	base   string
}

func (s *sandboxManagerContextSink) AddFile(path string, data []byte, mode uint32) error {
	target := filepath.Join(s.base, filepath.FromSlash(path))
	return s.client.FileCreate(s.ctx, target, data, mode)
}

func initSandboxContext(ctx context.Context, params Params, sbx sandbox.Instance, run *tool.RunContext, client *hostmanager.SandboxClient) error {
	if params.ContextFiles == nil {
		return fmt.Errorf("context files service is not configured")
	}
	contextDir := sbx.TobyContextDir()
	if err := client.FileDelete(ctx, contextDir, true); err != nil {
		return err
	}
	sink := &sandboxManagerContextSink{ctx: ctx, client: client, base: contextDir}
	run.ContextFiles = params.ContextFiles.NewEmittingSession(contextDir, sink)
	for _, service := range contextinit.Ordered(params.ContextInit) {
		if err := service.InitContext(ctx, run); err != nil {
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

func waitSandboxManagerAfterError(ctx context.Context, exit *sandboxManagerExit, client *hostmanager.SandboxClient, err error) error {
	_ = terminateSandboxManager(ctx, client, exit)
	return err
}

func terminateSandboxManager(ctx context.Context, client *hostmanager.SandboxClient, exit *sandboxManagerExit) error {
	if client != nil {
		if err := client.Terminate(ctx); err != nil {
			return err
		}
	}
	select {
	case <-exit.done:
		result := exit.result()
		if result.err != nil {
			return result.err
		}
		if result.exitCode != 0 {
			return exitcode.Code(result.exitCode)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
