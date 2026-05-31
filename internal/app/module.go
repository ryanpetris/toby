package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"petris.dev/toby/internal/cli"
	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/config/toby"
	"petris.dev/toby/internal/context/files"
	"petris.dev/toby/internal/context/setup"
	"petris.dev/toby/internal/control/hostmanager"
	"petris.dev/toby/internal/control/mcpserver"
	"petris.dev/toby/internal/control/sandboxmanager"
	"petris.dev/toby/internal/platform/executil"
	"petris.dev/toby/internal/sandbox"
	sandboxbubblewrap "petris.dev/toby/internal/sandbox/bubblewrap"
	sandboxdocker "petris.dev/toby/internal/sandbox/docker"
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
		hostmanager.Module(),
		mcpserver.Module(),
		tools.Module(),
		sandbox.Module(),
		sandboxbubblewrap.Module(),
		sandboxdocker.Module(),
		sandboxmanager.Module(),
		fx.Provide(
			config.NewPaths,
			executil.NewProcessRunner,
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
	SandboxService *sandbox.SandboxService
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
		SandboxService: params.SandboxService,
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
