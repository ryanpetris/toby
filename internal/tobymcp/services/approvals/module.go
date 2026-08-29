package approvalsservice

// fx wiring for the approvals service.

import (
	"go.uber.org/fx"

	"petris.dev/toby/internal/tobymcp"
)

type serviceResult struct {
	fx.Out

	Contributor tobymcp.Contributor `group:"mcpContributors"`
}

// NewService contributes the approvals MCP tools to Fx.
func NewService() serviceResult {
	return serviceResult{Contributor: Service{}}
}

// Module provides the approvals contributor to the MCP contributor group.
func Module() fx.Option {
	return fx.Module("tobymcp.approvalsservice", fx.Provide(NewService))
}
