package gitservice

// fx wiring for the git service.

import (
	"go.uber.org/fx"

	"petris.dev/toby/control/mcpserver"
)

type serviceResult struct {
	fx.Out

	Service mcpserver.Service `group:"mcp.services"`
}

func NewService() serviceResult {
	return serviceResult{Service: Service{}}
}

// Module provides the git service into the MCP service group.
func Module() fx.Option {
	return fx.Module("mcpserver.gitservice", fx.Provide(NewService))
}
