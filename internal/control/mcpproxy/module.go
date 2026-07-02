// fx wiring for the per-project MCP proxy layer. It provides the registration Service;
// the shared backend registry (internal/daemon/resource) is supplied from the daemon
// root into the per-project graph.

package mcpproxy

import "go.uber.org/fx"

func Module() fx.Option {
	return fx.Module("mcpproxy", fx.Provide(NewService))
}
