package app

import (
	"context"
	"errors"
	"io"
	"os"

	"petris.dev/toby/config"
	"petris.dev/toby/config/toby"
	"petris.dev/toby/container/engine"
	"petris.dev/toby/container/mount"
	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/control/host"
	"petris.dev/toby/control/methods/git"
	"petris.dev/toby/diagnostic/exitcode"
	"petris.dev/toby/internal/dirty/cli/session"
	contextinit "petris.dev/toby/internal/dirty/context/setup"
	"petris.dev/toby/internal/dirty/control/mcpproxy"
	"petris.dev/toby/internal/dirty/control/mcpserver"
	"petris.dev/toby/internal/dirty/sandbox"
	sandboxdocker "petris.dev/toby/internal/dirty/sandbox/docker"
	"petris.dev/toby/internal/dirty/toolwiring"
	"petris.dev/toby/lifecycle"
	"petris.dev/toby/platform/executil"
	"petris.dev/toby/tools"

	"go.uber.org/dig"
	"go.uber.org/fx"
)

type sessionRunnerParams struct {
	fx.In

	Registry *tools.Registry
	Paths    config.Paths
	Config   *tobyconfig.Service
}

type executionSessionRunner struct {
	registry *tools.Registry
	paths    config.Paths
	config   *tobyconfig.Service
	stderr   io.Writer
}

func newSessionRunner(params sessionRunnerParams) session.Runner {
	return &executionSessionRunner{registry: params.Registry, paths: params.Paths, config: params.Config, stderr: os.Stderr}
}

func (r *executionSessionRunner) Run(ctx context.Context, opts *tools.Options, extra, requestedTools []string, primary string) error {
	effectiveOpts := session.ApplySandboxDefaults(opts, r.config)
	selected, err := r.registry.Build(requestedTools, primary)
	if err != nil {
		return err
	}
	toolModule, err := toolwiring.SelectedModule(selected.OrderedToolNames())
	if err != nil {
		return err
	}
	runtimeModule, err := executionRuntimeModule(effectiveOpts.SandboxRuntime)
	if err != nil {
		return err
	}

	var params session.Params
	app := fx.New(
		fx.NopLogger,
		fx.Supply(r.paths, r.config),
		host.Module(),
		engine.Module(),
		mount.Module(),
		mcpproxy.Module(),
		mcpserver.Module(),
		sandbox.Module(),
		runtimeModule,
		toolModule,
		tools.Module(),
		lifecycle.Module(),
		fx.Provide(
			executil.NewProcessRunner,
			contextfiles.NewService,
			contextinit.NewLifecycleHooks,
			newExecutionSessionParams(r.stderr),
		),
		fx.Populate(&params),
	)
	if err := app.Err(); err != nil {
		return fxRootCause(err)
	}
	startCtx, cancel := context.WithTimeout(ctx, app.StartTimeout())
	startErr := app.Start(startCtx)
	cancel()
	if startErr != nil {
		return fxRootCause(startErr)
	}
	runErr := session.Run(ctx, params, &effectiveOpts, extra, requestedTools, primary)
	stopCtx, cancel := context.WithTimeout(context.Background(), app.StopTimeout())
	stopErr := app.Stop(stopCtx)
	cancel()
	if runErr != nil {
		return runErr
	}
	return fxRootCause(stopErr)
}

func executionRuntimeModule(runtime string) (fx.Option, error) {
	switch runtime {
	case "", sandbox.RuntimeDocker:
		return sandboxdocker.Module(), nil
	default:
		return nil, exitcode.New(2, "unknown sandbox runtime: %s", runtime)
	}
}

type executionSessionParams struct {
	fx.In

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
	Runner         *lifecycle.Runner
}

func newExecutionSessionParams(stderr io.Writer) func(executionSessionParams) session.Params {
	return func(params executionSessionParams) session.Params {
		return session.Params{
			Registry:       params.Registry,
			SandboxFactory: params.SandboxFactory,
			SandboxService: params.SandboxService,
			Paths:          params.Paths,
			ContextFiles:   params.ContextFiles,
			HostManager:    params.HostManager,
			Git:            params.Git,
			MCPProxy:       params.MCPProxy,
			MCPServer:      params.MCPServer,
			TobyConfig:     params.TobyConfig,
			Stderr:         stderr,
			Runner:         params.Runner,
		}
	}
}

func fxRootCause(err error) error {
	if err == nil {
		return nil
	}
	if cause := dig.RootCause(err); cause != nil {
		var digErr dig.Error
		if !errors.As(cause, &digErr) {
			return cause
		}
	}
	return err
}
