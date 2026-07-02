package home

// fx wiring for the daemon-root home registry.

import "go.uber.org/fx"

// Module registers the shared home-container Registry in the daemon graph.
func Module() fx.Option {
	return fx.Module("daemon.home", fx.Provide(NewRegistry))
}
