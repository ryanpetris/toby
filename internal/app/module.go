package app

import (
	"context"
	"os"

	"petris.dev/toby/internal/cli"
	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/contextfiles"
	"petris.dev/toby/internal/contextinit"
	"petris.dev/toby/internal/executil"
	"petris.dev/toby/internal/hostmanager"
	"petris.dev/toby/internal/mcpserver"
	"petris.dev/toby/internal/opencodeconfig"
	"petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/sandboxmanager"
	"petris.dev/toby/internal/tobyconfig"
	"petris.dev/toby/internal/tool"
	"petris.dev/toby/internal/tools"

	"github.com/spf13/cobra"
	"go.uber.org/fx"
)

type args []string

func Module() fx.Option {
	return fx.Options(
		fx.NopLogger,
		hostmanager.Module(),
		mcpserver.Module(),
		tools.Module(),
		sandboxmanager.Module(),
		fx.Provide(
			config.NewPaths,
			executil.NewProcessRunner,
			opencodeconfig.NewRenderer,
			sandbox.NewFactory,
			contextfiles.NewService,
			tobyconfig.New,
			contextinit.NewServices,
			tool.NewRegistry,
			newArgs,
			newRootCommand,
		),
		fx.Invoke(runCLI),
	)
}

func newArgs() args {
	if len(os.Args) <= 1 {
		return nil
	}
	return append([]string(nil), os.Args[1:]...)
}

type rootCommandParams struct {
	fx.In

	Registry       *tool.Registry
	Factory        sandbox.Factory
	Paths          config.Paths
	ContextFiles   *contextfiles.Service
	ContextInit    []contextinit.Registration `group:"toby.context.init"`
	HostManager    *hostmanager.HostManager
	SandboxManager *sandboxmanager.Runner
	MCPServer      *mcpserver.Runner
	Config         *tobyconfig.Service
	Args           args
}

func newRootCommand(params rootCommandParams) *cobra.Command {
	cliParams := cli.Params{
		Registry:       params.Registry,
		SandboxFactory: params.Factory,
		Paths:          params.Paths,
		ContextFiles:   params.ContextFiles,
		ContextInit:    params.ContextInit,
		HostManager:    params.HostManager,
		SandboxManager: params.SandboxManager,
		MCPServer:      params.MCPServer,
		TobyConfig:     params.Config,
		Args:           []string(params.Args),
		Stdout:         os.Stdout,
		Stderr:         os.Stderr,
	}
	return cli.NewRootCommand(cliParams)
}

func runCLI(lc fx.Lifecycle, shutdowner fx.Shutdowner, cmd *cobra.Command) {
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			go func() {
				code := cli.ExecuteAndReport(cmd)
				_ = shutdowner.Shutdown(fx.ExitCode(code))
			}()
			return nil
		},
	})
}
