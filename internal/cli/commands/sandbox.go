package commands

import (
	"fmt"
	"os"

	"petris.dev/toby/internal/control/sandboxmanager"
	"petris.dev/toby/internal/diagnostic/exitcode"

	"github.com/spf13/cobra"
)

func newSandboxCommand(params Params) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "sandbox",
		Short:  "Run Toby sandbox internals.",
		Hidden: os.Getenv("TOBY_SANDBOX") != "1",
	}
	cmd.AddCommand(newSandboxManagerCommand(params.SandboxManager))
	cmd.AddCommand(newSandboxGitCommand())
	return cmd
}

func newSandboxManagerCommand(runner *sandboxmanager.Runner) *cobra.Command {
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
			return runner.Run(cmd.Context(), "")
		},
	}
}
