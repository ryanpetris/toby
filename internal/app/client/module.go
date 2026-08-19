package client

// Fx wiring for the Toby launch and management CLI.

import (
	"context"
	"os"

	"petris.dev/toby/internal/agent"
	"petris.dev/toby/internal/approval"
	"petris.dev/toby/internal/cli"
	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/config/app"
	"petris.dev/toby/internal/config/session"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/diagnostic/warning"
	"petris.dev/toby/internal/hostaction/methods/git"
	"petris.dev/toby/internal/lifecycle"
	"petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/sandbox/bwrap"
	"petris.dev/toby/internal/session/run"
	"petris.dev/toby/internal/shutdown"
	"petris.dev/toby/internal/status"
	"petris.dev/toby/internal/tools"
	"petris.dev/toby/internal/tools/wiring"

	"github.com/spf13/cobra"
	"go.uber.org/fx"
)

type args []string

// Module provides the package's components to Fx.
func Module() fx.Option {
	return fx.Options(
		processModule(),
		fx.StopTimeout(shutdown.ProcessFinalizationGrace),
		fx.Invoke(runCLI),
	)
}

// processModule composes the process-wide graph without starting the CLI.
func processModule() fx.Option {
	return fx.Options(
		fx.NopLogger,
		diagnostic.Module(),
		shutdown.Module(),
		agent.ClientModule(),
		approval.Module(),
		git.Module(),
		status.Module(),
		lifecycle.Module(),
		wiring.Module,
		tools.Module(),
		fx.Provide(
			config.NewPaths,
			newBaseConfig,
			appconfig.NewLaunchHolder,
			newWarningService,
			sessionconfig.NewHolder,
			fx.Annotate(
				bwrap.NewToolService,
				fx.As(new(sandbox.Service)),
				fx.As(fx.Self()),
			),
			newArgs,
			newSessionRunner,
			newRootCommand,
			newCLIResult,
		),
	)
}

func newWarningService(
	diagnostics *diagnostic.Service,
	holder *appconfig.LaunchHolder,
) *warning.Service {
	return warning.NewService(
		diagnostics.Logger("warning"),
		func() warning.Suppression {
			current := holder.Current()
			if current == nil {
				return warning.Suppression{}
			}
			return current.Settings().SuppressWarnings
		},
	)
}

func newArgs() args {
	if len(os.Args) <= 1 {
		return nil
	}
	return append([]string(nil), os.Args[1:]...)
}

func newBaseConfig(
	paths config.Paths,
	arguments args,
) (*appconfig.Service, error) {
	if cli.IsConfigFreeInvocation(arguments) {
		return appconfig.NewDefaults(paths), nil
	}

	return appconfig.New(paths)
}

type rootCommandParams struct {
	fx.In

	Registry      *tools.Registry
	Paths         config.Paths
	Config        *appconfig.Service
	Agent         *agent.Client
	Diagnostics   *diagnostic.Service
	Status        *status.Service
	Warnings      *warning.Service
	SessionRunner run.Runner
	Args          args
}

type sessionRunnerParams struct {
	fx.In

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
}

func newRootCommand(params rootCommandParams) *cobra.Command {
	cliParams := cli.Params{
		Registry:      params.Registry,
		Paths:         params.Paths,
		TobyConfig:    params.Config,
		Agent:         params.Agent,
		Diagnostics:   params.Diagnostics,
		Status:        params.Status,
		Warnings:      params.Warnings,
		SessionRunner: params.SessionRunner,
		Args:          []string(params.Args),
		Stdin:         os.Stdin,
		Stdout:        os.Stdout,
		Stderr:        params.Diagnostics.Stderr(),
	}
	return cli.NewRootCommand(cliParams)
}

func runCLI(
	lc fx.Lifecycle,
	cmd *cobra.Command,
	result *cliResult,
	shutdownService *shutdown.Service,
) {
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			go func() {
				result.ch <- cli.ExecuteAndReport(
					shutdownService.Context(),
					cmd,
				)
			}()
			return nil
		},
	})
}
