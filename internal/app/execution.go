package app

// The fx session runner: builds the per-launch fx graph (sessionModules), starts
// it, and invokes run.Run for each launch.

import (
	"context"
	"errors"
	"io"
	"os"

	"petris.dev/toby/config"
	"petris.dev/toby/config/session"
	"petris.dev/toby/container/engine"
	"petris.dev/toby/container/mount"
	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/internal/approval"
	"petris.dev/toby/internal/config/app"
	contextinit "petris.dev/toby/internal/context/setup"
	"petris.dev/toby/internal/control/host"
	"petris.dev/toby/internal/control/mcpproxy"
	"petris.dev/toby/internal/control/mcpserver"
	gitservice "petris.dev/toby/internal/control/mcpserver/services/git"
	sessionservice "petris.dev/toby/internal/control/mcpserver/services/session"
	"petris.dev/toby/internal/control/methods/git"
	"petris.dev/toby/internal/lifecycle"
	"petris.dev/toby/internal/session/resolve"
	"petris.dev/toby/internal/session/run"
	"petris.dev/toby/internal/status"
	"petris.dev/toby/internal/tools/wiring"
	"petris.dev/toby/platform/executil"
	"petris.dev/toby/providers"
	"petris.dev/toby/providers/anthropic"
	"petris.dev/toby/providers/openai"
	sandbox "petris.dev/toby/sandbox/runtime"
	"petris.dev/toby/tools"

	"go.uber.org/dig"
	"go.uber.org/fx"
)

type sessionRunnerParams struct {
	fx.In

	Registry *tools.Registry
	Paths    config.Paths
	Config   *appconfig.Service
}

type executionSessionRunner struct {
	registry *tools.Registry
	paths    config.Paths
	config   *appconfig.Service
	stderr   io.Writer
}

var _ run.Runner = (*executionSessionRunner)(nil)

func newSessionRunner(params sessionRunnerParams) run.Runner {
	return &executionSessionRunner{registry: params.Registry, paths: params.Paths, config: params.Config, stderr: os.Stderr}
}

func (r *executionSessionRunner) Run(ctx context.Context, opts *tools.Options, overrides appconfig.LaunchOverrides, extra, requestedTools []string, primary string) error {
	// Fold the launch's config-corresponding overrides (CLI flags + launch-config
	// file) into a per-launch effective config; the whole graph reads from it.
	effectiveConfig := r.config.WithOverrides(overrides)
	selected, err := r.registry.Build(requestedTools, primary)
	if err != nil {
		return err
	}
	toolModule, err := wiring.SelectedModule(selected.OrderedToolNames())
	if err != nil {
		return err
	}

	var params run.Params
	options := append(sessionModules(toolModule, r.stderr),
		fx.NopLogger,
		fx.Supply(r.paths, effectiveConfig),
		fx.Populate(&params),
	)
	app := fx.New(options...)
	if err := app.Err(); err != nil {
		return fxRootCause(err)
	}
	startCtx, cancel := context.WithTimeout(ctx, app.StartTimeout())
	startErr := app.Start(startCtx)
	cancel()
	if startErr != nil {
		return fxRootCause(startErr)
	}
	runErr := run.Run(ctx, params, opts, extra, requestedTools, primary)
	stopCtx, cancel := context.WithTimeout(context.Background(), app.StopTimeout())
	stopErr := app.Stop(stopCtx)
	cancel()
	if runErr != nil {
		return runErr
	}
	return fxRootCause(stopErr)
}

// sessionModules is the fx graph for one launch: host services, the selected
// tools, the sandbox runtime, the lifecycle runner, the provider registry, and
// the session-config resolver. It excludes the run-specific bindings (paths,
// config, populate target) so the graph can be validated in isolation.
func sessionModules(toolModule fx.Option, stderr io.Writer) []fx.Option {
	return []fx.Option{
		host.Module(),
		approval.Module(),
		engine.Module(),
		mount.Module(),
		mcpproxy.Module(),
		mcpserver.Module(),
		gitservice.Module(),
		sessionservice.Module(),
		sandbox.Module(),
		status.Module(),
		toolModule,
		tools.Module(),
		lifecycle.Module(),
		providers.Module(),
		openai.Module(),
		anthropic.Module(),
		fx.Provide(
			executil.NewProcessRunner,
			contextfiles.NewService,
			contextinit.NewLifecycleHooks,
			sessionconfig.NewHolder,
			resolve.NewLifecycleHooks,
			newExecutionSessionParams(stderr),
		),
	}
}

type executionSessionParams struct {
	fx.In

	Registry       *tools.Registry
	SandboxFactory sandbox.Factory
	SandboxService *sandbox.SandboxService
	Engine         *engine.Service
	Paths          config.Paths
	ContextFiles   *contextfiles.Service
	HostManager    *host.Service
	Git            *git.Service
	Approval       *approval.Service
	MCPProxy       *mcpproxy.Service
	MCPServer      *mcpserver.Runner
	TobyConfig     *appconfig.Service
	Status         *status.Service
	Runner         *lifecycle.Runner
}

func newExecutionSessionParams(stderr io.Writer) func(executionSessionParams) run.Params {
	return func(params executionSessionParams) run.Params {
		return run.Params{
			Registry:       params.Registry,
			SandboxFactory: params.SandboxFactory,
			SandboxService: params.SandboxService,
			Engine:         params.Engine,
			Paths:          params.Paths,
			ContextFiles:   params.ContextFiles,
			HostManager:    params.HostManager,
			Git:            params.Git,
			Approval:       params.Approval,
			MCPProxy:       params.MCPProxy,
			MCPServer:      params.MCPServer,
			TobyConfig:     params.TobyConfig,
			Status:         params.Status,
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
