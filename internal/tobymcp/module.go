package tobymcp

// Fx wiring for native MCP server contributors.

import (
	"petris.dev/toby/internal/diagnostic"

	"go.uber.org/fx"
)

// RunnerParams contains MCP contributors supplied through Fx.
type RunnerParams struct {
	fx.In

	Contributors []Contributor `group:"mcpContributors"`
	Diagnostics  *diagnostic.Service
}

// Module provides the package's components to Fx.
func Module() fx.Option {
	return fx.Module(
		"mcpserver",
		fx.Provide(NewRunner),
	)
}
