package status

// fx wiring for the startup-status Service.

import "go.uber.org/fx"

// Module provides the per-launch status Service as a singleton.
func Module() fx.Option {
	return fx.Module("status",
		fx.Provide(NewService),
	)
}
