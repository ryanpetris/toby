package gitservice

// fx wiring for the git service.

import (
	"go.uber.org/fx"

	"petris.dev/toby/internal/tobymcp"
)

type serviceResult struct {
	fx.Out

	Contributor tobymcp.Contributor `group:"mcpContributors"`
}

// NewService contributes Git MCP tools to Fx.
func NewService() serviceResult {
	return serviceResult{Contributor: Service{}}
}

// Module provides the Git contributor to the MCP contributor group.
func Module() fx.Option {
	return fx.Module("tobymcp.gitservice", fx.Provide(NewService))
}
