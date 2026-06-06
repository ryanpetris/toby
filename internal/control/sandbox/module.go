package sandbox

// fx wiring for the in-sandbox proxy-only manager.

import (
	"go.uber.org/fx"
)

func Module() fx.Option {
	return fx.Module(
		"control.sandbox",
		fx.Provide(
			NewRunner,
		),
	)
}
