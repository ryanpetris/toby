package app

import (
	"context"
	"os"

	"petris.dev/toby/internal/cli"
	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/executil"
	"petris.dev/toby/internal/opencodeconfig"
	"petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/staticfiles"
	"petris.dev/toby/internal/tool"
	"petris.dev/toby/internal/tools"

	"github.com/spf13/cobra"
	"go.uber.org/fx"
)

type args []string

func Module() fx.Option {
	return fx.Options(
		fx.NopLogger,
		tools.Module(),
		fx.Provide(
			config.NewPaths,
			executil.NewProcessRunner,
			opencodeconfig.NewRenderer,
			sandbox.NewFactory,
			staticfiles.NewService,
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

func newRootCommand(registry *tool.Registry, factory sandbox.Factory, staticFiles *staticfiles.Service, renderer *opencodeconfig.Renderer, argv args) *cobra.Command {
	return cli.NewRootCommand(cli.Params{
		Registry:         registry,
		SandboxFactory:   factory,
		StaticFiles:      staticFiles,
		OpenCodeRenderer: renderer,
		Args:             []string(argv),
		Stdout:           os.Stdout,
		Stderr:           os.Stderr,
	})
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
