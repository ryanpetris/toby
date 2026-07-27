package sessionservice

// fx wiring for the Toby session service.

import (
	"go.uber.org/fx"

	"petris.dev/toby/internal/tobymcp"
)

type serviceResult struct {
	fx.Out

	Contributor tobymcp.Contributor `group:"mcpContributors"`
}

// NewService contributes session introspection to Fx.
func NewService() serviceResult {
	return serviceResult{Contributor: Service{}}
}

// Module provides session introspection to the MCP contributor group.
func Module() fx.Option {
	return fx.Module("tobymcp.sessionservice", fx.Provide(NewService))
}
