package app

import (
	"context"
	"os"
	"path/filepath"

	"petris.dev/toby/internal/cli"
	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/contextfiles"
	"petris.dev/toby/internal/executil"
	"petris.dev/toby/internal/opencodeconfig"
	"petris.dev/toby/internal/sandbox"
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
		tools.Module(),
		fx.Provide(
			config.NewPaths,
			executil.NewProcessRunner,
			opencodeconfig.NewRenderer,
			sandbox.NewFactory,
			contextfiles.NewService,
			tobyconfig.New,
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

func newRootCommand(registry *tool.Registry, factory sandbox.Factory, contextFiles *contextfiles.Service, cfg *tobyconfig.Service, argv args) *cobra.Command {
	params := cli.Params{
		Registry:       registry,
		SandboxFactory: factory,
		ContextFiles:   contextFiles,
		TobyConfig:     cfg,
		Args:           []string(argv),
		Stdout:         os.Stdout,
		Stderr:         os.Stderr,
	}
	if filepath.Base(os.Args[0]) == "toby-sandbox" {
		return cli.NewSandboxRootCommand(params)
	}
	return cli.NewRootCommand(params)
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
