// Package app is Toby's composition root: it builds the root fx application,
// wires the host services and the session runner, runs the Cobra CLI, and is the
// entry point main.go calls.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"petris.dev/toby/config"
	"petris.dev/toby/internal/cli"
	"petris.dev/toby/internal/config/app"
	"petris.dev/toby/internal/control/sandbox"
	"petris.dev/toby/internal/session/run"
	"petris.dev/toby/internal/tools/wiring"
	"petris.dev/toby/tools"

	"github.com/spf13/cobra"
	"go.uber.org/dig"
	"go.uber.org/fx"
)

func Run() {
	var result *cliResult
	app := fx.New(Module(), fx.Populate(&result))
	os.Exit(runApp(app, result, os.Stderr))
}

// cliResult carries the CLI's exit code from the runCLI goroutine back to runApp.
// runApp blocks on the channel so the process stays alive until the command — and
// the deferred sandbox teardown it unwinds on a signal — has fully completed. The
// buffer of one keeps the goroutine from blocking on send if runApp has already
// bailed out on a start error and is no longer receiving.
type cliResult struct{ ch chan int }

func newCLIResult() *cliResult { return &cliResult{ch: make(chan int, 1)} }

func runApp(app *fx.App, result *cliResult, stderr io.Writer) int {
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

	// Block until the command finishes — including, on SIGTERM, the teardown that
	// stops the sandbox container. fx is deliberately not given the signal (we never
	// call app.Wait), so it cannot race that teardown to os.Exit and orphan the
	// container; runCLI owns SIGTERM and reports the code here.
	code := <-result.ch

	stopCtx, cancel := context.WithTimeout(context.Background(), app.StopTimeout())
	stopErr := app.Stop(stopCtx)
	cancel()
	if stopErr != nil {
		reportAppError(stderr, stopErr)
		return 1
	}
	return code
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
		sandbox.Module(),
		wiring.PlanningModule(),
		tools.Module(),
		fx.Provide(
			config.NewPaths,
			appconfig.New,
			newArgs,
			newSessionRunner,
			newRootCommand,
			newCLIResult,
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

	Registry       *tools.Registry
	Paths          config.Paths
	Config         *appconfig.Service
	SandboxManager *sandbox.Runner
	SessionRunner  run.Runner
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

func runCLI(lc fx.Lifecycle, cmd *cobra.Command, result *cliResult) {
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			go func() {
				// The launch owns SIGTERM: a stop (e.g. systemd stopping the service)
				// cancels the command's context, so run.Run unwinds and its deferred
				// RunStop tears the sandbox container down before the process exits.
				// runApp blocks on result.ch until that completes. SIGINT (Ctrl-C) is
				// left alone — in an interactive launch the terminal is in raw mode, so
				// Ctrl-C reaches the tool as a PTY byte rather than a signal to toby.
				ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
				defer stop()
				result.ch <- cli.ExecuteAndReport(ctx, cmd)
			}()
			return nil
		},
	})
}
