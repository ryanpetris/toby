// fx wiring: the daemon-root shared MCP backend registry and its Docker sidecar runner.
// One registry serves every project; the sidecar containers it owns are shared.

package resource

import "go.uber.org/fx"

// Module provides the shared Registry and its DockerRunner.
func Module() fx.Option {
	return fx.Module("resource", fx.Provide(NewDockerRunner, NewRegistry))
}
