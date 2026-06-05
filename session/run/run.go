// Package run executes one Toby launch end to end: it builds the sandbox, stands
// up the host control endpoint and Toby MCP proxy, starts the sandbox manager,
// runs the requested tool, and tears everything down. Run is the entry point; the
// app package's session runner supplies the resolved Params and invokes it.
package run

import (
	"context"
	"fmt"
	"net/http"

	"petris.dev/toby/control"
	"petris.dev/toby/diagnostic/exitcode"
	"petris.dev/toby/lifecycle"
	"petris.dev/toby/platform/environ"
	sandboxbinary "petris.dev/toby/sandbox/binary"
	sandbox "petris.dev/toby/sandbox/runtime"
	"petris.dev/toby/tools"
)

func Run(ctx context.Context, params Params, opts *tools.Options, extra, requestedTools []string, primary string) error {
	effectiveOpts := ApplySandboxDefaults(opts, params.TobyConfig)
	opts = &effectiveOpts
	if err := prepareConfiguredProjects(params.Stderr, params.Paths.Home, opts); err != nil {
		return err
	}
	if params.Engine == nil {
		return fmt.Errorf("container engine is not configured")
	}
	if err := params.Engine.Ping(ctx); err != nil {
		return exitcode.New(2, "docker socket not reachable (is the daemon running, or DOCKER_HOST set?): %v", err)
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
