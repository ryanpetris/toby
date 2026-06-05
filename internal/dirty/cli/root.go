package cli

import (
	"petris.dev/toby/internal/dirty/cli/commands"

	"github.com/spf13/cobra"
)

type Params = commands.Params

func NewRootCommand(params Params) *cobra.Command {
	return commands.NewRootCommand(params)
}

func ExecuteAndReport(cmd *cobra.Command) int {
	return commands.ExecuteAndReport(cmd)
}
