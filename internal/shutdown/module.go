package shutdown

// Fx wiring for process-wide signal and shutdown coordination.

import "go.uber.org/fx"

// Module provides the shutdown service as a process singleton.
func Module() fx.Option {
	return fx.Module(
		"shutdown",
		fx.Provide(NewService),
	)
}
