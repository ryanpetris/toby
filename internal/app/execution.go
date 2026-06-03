package app

import (
	"context"
	"errors"
	"io"
	"os"

	"petris.dev/toby/internal/cli/session"
	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/config/toby"
	contextfiles "petris.dev/toby/internal/context/files"
	contextinit "petris.dev/toby/internal/context/setup"
	"petris.dev/toby/internal/control/hostmanager"
	"petris.dev/toby/internal/control/mcpproxy"
	"petris.dev/toby/internal/control/mcpserver"
	"petris.dev/toby/internal/diagnostic/exitcode"
	"petris.dev/toby/internal/platform/executil"
	"petris.dev/toby/internal/sandbox"
	sandboxbubblewrap "petris.dev/toby/internal/sandbox/bubblewrap"
	sandboxdocker "petris.dev/toby/internal/sandbox/docker"
	"petris.dev/toby/internal/tools"
	"petris.dev/toby/internal/tools/tool"

	"go.uber.org/dig"
	"go.uber.org/fx"
)

type sessionRunnerParams struct {
	fx.In

	Registry *tool.Registry
	Paths    config.Paths
	Config   *tobyconfig.Service
}

type executionSessionRunner struct {
	registry *tool.Registry
	paths    config.Paths
	config   *tobyconfig.Service
	stderr   io.Writer
}

func newSessionRunner(params sessionRunnerParams) session.Runner {
	return &executionSessionRunner{registry: params.Registry, paths: params.Paths, config: params.Config, stderr: os.Stderr}
}

func (r *executionSessionRunner) Run(ctx context.Context, opts *tool.CommandOptions, extra, requestedTools []string, primary string) error {
	effectiveOpts := session.ApplySandboxDefaults(opts, r.config)
	selected, err := r.registry.Build(requestedTools, primary)
	if err != nil {
		return err
	}
	toolModule, err := tools.SelectedModule(selected.OrderedToolNames())
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
		hostmanager.Module(),
		mcpproxy.Module(),
		mcpserver.Module(),
		sandbox.Module(),
		runtimeModule,
		toolModule,
		fx.Provide(
			executil.NewProcessRunner,
			contextfiles.NewService,
			contextinit.NewLifecycleHooks,
			tool.NewRegistry,
			tool.NewLifecycleHooks,
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
	case "":
		return fx.Options(sandboxdocker.Module(), sandboxbubblewrap.Module()), nil
	case sandbox.RuntimeDocker:
		return sandboxdocker.Module(), nil
	case sandbox.RuntimeBubblewrap:
		return sandboxbubblewrap.Module(), nil
	default:
		return nil, exitcode.New(2, "unknown sandbox runtime: %s", runtime)
	}
}

type executionSessionParams struct {
	fx.In

	Registry       *tool.Registry
	SandboxFactory sandbox.Factory
	SandboxService *sandbox.SandboxService
	Paths          config.Paths
	ContextFiles   *contextfiles.Service
	HostManager    *hostmanager.HostManager
	MCPProxy       *mcpproxy.Service
	MCPServer      *mcpserver.Runner
	TobyConfig     *tobyconfig.Service

	HostInitHooks     []tool.LifecycleHook `group:"toby.lifecycle.host.init"`
	MountInitHooks    []tool.LifecycleHook `group:"toby.lifecycle.sandbox.mount.init"`
	ContextSetupHooks []tool.LifecycleHook `group:"toby.lifecycle.sandbox.context.setup"`
	ContextInitHooks  []tool.LifecycleHook `group:"toby.lifecycle.sandbox.context.init"`
	SandboxInitHooks  []tool.LifecycleHook `group:"toby.lifecycle.sandbox.init"`
	InstallHooks      []tool.LifecycleHook `group:"toby.lifecycle.sandbox.install"`
	UpgradeHooks      []tool.LifecycleHook `group:"toby.lifecycle.sandbox.upgrade"`
}

func newExecutionSessionParams(stderr io.Writer) func(executionSessionParams) session.Params {
	return func(params executionSessionParams) session.Params {
		return session.Params{
			Registry:          params.Registry,
			SandboxFactory:    params.SandboxFactory,
			SandboxService:    params.SandboxService,
			Paths:             params.Paths,
			ContextFiles:      params.ContextFiles,
			HostManager:       params.HostManager,
			MCPProxy:          params.MCPProxy,
			MCPServer:         params.MCPServer,
			TobyConfig:        params.TobyConfig,
			Stderr:            stderr,
			HostInitHooks:     params.HostInitHooks,
			MountInitHooks:    params.MountInitHooks,
			ContextSetupHooks: params.ContextSetupHooks,
			ContextInitHooks:  params.ContextInitHooks,
			SandboxInitHooks:  params.SandboxInitHooks,
			InstallHooks:      params.InstallHooks,
			UpgradeHooks:      params.UpgradeHooks,
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
