package cli

// The `toby sandbox` subcommand tree: the in-sandbox manager commands, hidden on
// the host.

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"petris.dev/toby/diagnostic/exitcode"
	"petris.dev/toby/internal/control/sandbox"

	"github.com/spf13/cobra"
)

func newSandboxCommand(params Params) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "sandbox",
		Short:  "Run Toby sandbox internals.",
		Hidden: os.Getenv("TOBY_SANDBOX") != "1",
	}
	cmd.AddCommand(newSandboxIdleCommand())
	cmd.AddCommand(newSandboxManagerCommand(params.SandboxManager))
	return cmd
}

// newSandboxIdleCommand is the container's main process: it does nothing but keep
// the container alive until the host stops it, so `docker logs` stays empty. The
// proxy manager runs alongside it as a docker exec, whose stdio carries the gRPC
// link instead of the container's own stdout.
func newSandboxIdleCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "idle",
		Short: "Keep the Toby sandbox container alive.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return exitcode.New(2, "idle does not accept arguments")
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			<-ctx.Done()
			return nil
		},
	}
}

func newSandboxManagerCommand(runner *sandbox.Runner) *cobra.Command {
	return &cobra.Command{
		Use:   "manager",
		Short: "Run the Toby sandbox manager.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return exitcode.New(2, "manager does not accept arguments")
			}
			if runner == nil {
				return fmt.Errorf("sandbox manager runner is not configured")
			}
			return runner.Run(cmd.Context())
		},
	}
}
