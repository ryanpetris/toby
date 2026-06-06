package mcpproxy

// fx wiring for the MCP proxy: provides the Docker sidecar runner and the proxy Service.

import "go.uber.org/fx"

func Module() fx.Option {
	return fx.Module("mcpproxy", fx.Provide(NewDockerRunner, NewService))
}
