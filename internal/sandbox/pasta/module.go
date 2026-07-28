package pasta

// Fx wiring for the process-wide Pasta launch service.

import "go.uber.org/fx"

// Module provides the Pasta launch service.
func Module() fx.Option {
	return fx.Module(
		"pasta",
		fx.Provide(NewService),
	)
}
