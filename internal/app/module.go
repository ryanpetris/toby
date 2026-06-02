package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"petris.dev/toby/internal/cli"
	"petris.dev/toby/internal/cli/session"
	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/config/toby"
	"petris.dev/toby/internal/control/sandboxmanager"
	"petris.dev/toby/internal/tools"
	"petris.dev/toby/internal/tools/tool"

	"github.com/spf13/cobra"
	"go.uber.org/dig"
	"go.uber.org/fx"
)

func Run() {
	os.Exit(runApp(fx.New(Module()), os.Stderr))
}

func runApp(app *fx.App, stderr io.Writer) int {
	if stderr == nil {
		stderr = os.Stderr
	}
	if err := app.Err(); err != nil {
		reportAppError(stderr, err)
		return 1
	}
	startCtx, cancel := context.WithTimeout(context.Background(), app.StartTimeout())
	startErr := app.Start(startCtx)
	cancel()
	if startErr != nil {
		reportAppError(stderr, startErr)
		return 1
	}
	signal := <-app.Wait()
	stopCtx, cancel := context.WithTimeout(context.Background(), app.StopTimeout())
	stopErr := app.Stop(stopCtx)
	cancel()
	if stopErr != nil {
		reportAppError(stderr, stopErr)
		return 1
	}
	return signal.ExitCode
}

func reportAppError(stderr io.Writer, err error) {
	if cause := dig.RootCause(err); cause != nil {
		var digErr dig.Error
		if !errors.As(cause, &digErr) {
			err = cause
		}
	}
	fmt.Fprintln(stderr, err)
}

type args []string

func Module() fx.Option {
	return fx.Options(
		fx.NopLogger,
		sandboxmanager.Module(),
		tools.PlanningModule(),
		fx.Provide(
			config.NewPaths,
			tobyconfig.New,
			tool.NewRegistry,
			newArgs,
			newSessionRunner,
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
	Paths          config.Paths
	Config         *tobyconfig.Service
	SandboxManager *sandboxmanager.Runner
	SessionRunner  session.Runner
	Args           args
}

func newRootCommand(params rootCommandParams) *cobra.Command {
	cliParams := cli.Params{
		Registry:       params.Registry,
		Paths:          params.Paths,
		TobyConfig:     params.Config,
		SandboxManager: params.SandboxManager,
		SessionRunner:  params.SessionRunner,
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
