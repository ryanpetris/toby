package lifecycle

// fx wiring for the launch lifecycle.

import "go.uber.org/fx"

// Module provides the Runner and startup-status Service.
func Module() fx.Option {
	return fx.Module("lifecycle",
		fx.Provide(NewRunner),
	)
}
