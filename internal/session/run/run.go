// Package run executes one Toby launch end to end: it builds the sandbox, stands
// up the host reverse proxy and Toby MCP server, starts the sandbox manager over a
// stdio gRPC link, runs the requested tool, and tears everything down. Run is the
// entry point; the app package's session runner supplies the resolved Params and
// invokes it.
package run

import (
	"context"
	"fmt"

	"petris.dev/toby/diagnostic/exitcode"
	"petris.dev/toby/internal/lifecycle"
	"petris.dev/toby/platform/environ"
	sandbox "petris.dev/toby/sandbox/runtime"
	"petris.dev/toby/tools"
)

func Run(ctx context.Context, params Params, opts *tools.Options, extra, requestedTools []string, primary string) error {
	if opts == nil {
		opts = &tools.Options{}
	}
	if err := prepareConfiguredProjects(params.Stderr, params.Paths.Home, opts, params.TobyConfig.Settings().SuppressWarnings); err != nil {
		return err
	}
	if params.Engine == nil {
		return fmt.Errorf("container engine is not configured")
	}
	if err := params.Engine.Ping(ctx); err != nil {
		return exitcode.New(2, "docker socket not reachable (is the daemon running, or DOCKER_HOST set?): %v", err)
	}
	sbx, err := params.SandboxFactory.FromOptions(opts, params.TobyConfig.Image(), params.TobyConfig.Build())
	if err != nil {
		return err
	}
	defer sbx.Cleanup()
	if params.SandboxService == nil {
		return fmt.Errorf("sandbox service is not configured")
	}
	params.SandboxService.Prepare(sbx)
	if err := params.SandboxService.ConfigureMounts(params.TobyConfig.MountProfile(), params.TobyConfig.ToolMountProfiles()); err != nil {
		return err
	}

	toolset, err := params.Registry.Build(requestedTools, primary)
	if err != nil {
		return err
	}
	lctx := lifecycle.Context{Options: opts, Stderr: params.Stderr, SuppressWarnings: params.TobyConfig.Settings().SuppressWarnings}
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

	// Proxied URLs point at the in-container proxy listener (a fixed loopback
	// address); the manager tunnels each connection to the host reverse proxy over
	// the gRPC stdio link.
	mcpURL, err := registerTobyMCPProxy(params, manager, opts, activeTools, primary)
	if err != nil {
		return err
	}
	params.SandboxService.SetTobyMCPURL(mcpURL)
	if params.MCPProxy != nil {
		if err := params.MCPProxy.Configure(ctx, params.TobyConfig, mcpDefaults(params.TobyConfig)); err != nil {
			return err
		}
		params.MCPProxy.StartAll(ctx)
		defer params.MCPProxy.StopAll(context.Background())
	}

	mounts := params.SandboxService.RuntimeMounts()
	binds := params.SandboxService.StartBinds()
	runSpec := sandbox.RunSpec{Env: env, Binds: binds, Mounts: mounts, Debug: params.TobyConfig.DebugEnabled()}
	return startRunSandbox(ctx, params, manager, sbx, env, runSpec, toolset, lctx, opts, extra)
}
