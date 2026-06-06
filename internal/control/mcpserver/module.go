package mcpserver

// fx wiring for the MCP server: provides the Runner that collects every
// registered service into one streamable-HTTP server.

import "go.uber.org/fx"

func Module() fx.Option {
	return fx.Module(
		"mcpserver",
		fx.Provide(NewRunner),
	)
}
