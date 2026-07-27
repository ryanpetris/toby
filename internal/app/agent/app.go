// Package agent is the tobyd composition root.
package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	agentservice "petris.dev/toby/internal/agent"
	"petris.dev/toby/internal/cli"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/executable"
	"petris.dev/toby/internal/version"

	"github.com/spf13/cobra"
	"go.uber.org/fx"
)

// Run assembles and executes the per-user Toby agent.
func Run() {
	if err := executable.CheckUnprivileged(); err != nil {
		_, writeErr := fmt.Fprintln(os.Stderr, err)
		diagnostic.DiscardError(
			"Fx construction is unavailable",
			"write early agent error",
			writeErr,
		)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(
		context.Background(),
		syscall.SIGINT,
		syscall.SIGTERM,
	)
	defer stop()

	command := newRootCommand(os.Args[1:], os.Stdout, os.Stderr)
	os.Exit(cli.ExecuteAndReport(ctx, command))
}

func newRootCommand(
	arguments []string,
	stdout io.Writer,
	stderr io.Writer,
) *cobra.Command {
	var persistent bool
	command := &cobra.Command{
		Use:           "tobyd",
		Short:         "Run the per-user Toby agent.",
		Version:       version.String(),
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return serve(
				command.Context(),
				persistent,
				command.ErrOrStderr(),
			)
		},
	}
	command.SetArgs(arguments)
	command.SetOut(stdout)
	command.SetErr(stderr)
	command.SetVersionTemplate("{{.Version}}\n")
	command.Flags().BoolVar(
		&persistent,
		"persistent",
		false,
		"Remain running when the agent has no active work.",
	)

	return command
}

func serve(
	ctx context.Context,
	persistent bool,
	stderr io.Writer,
) error {
	var service *agentservice.Service
	application := fx.New(
		module(),
		fx.Populate(&service),
	)
	if err := application.Err(); err != nil {
		return err
	}

	startContext, cancelStart := context.WithTimeout(
		context.Background(),
		application.StartTimeout(),
	)
	startErr := application.Start(startContext)
	cancelStart()
	if startErr != nil {
		return startErr
	}

	serveErr := service.Serve(
		ctx,
		agentservice.ServeOptions{Persistent: persistent},
	)
	if errors.Is(serveErr, agentservice.ErrAlreadyRunning) {
		_, writeErr := fmt.Fprintln(
			stderr,
			"Toby agent is already running.",
		)
		serveErr = writeErr
	}

	stopContext, cancelStop := context.WithTimeout(
		context.Background(),
		application.StopTimeout(),
	)
	stopErr := application.Stop(stopContext)
	cancelStop()

	return errors.Join(serveErr, stopErr)
}
