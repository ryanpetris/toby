package approval

// fx wiring for the approval service.

import "go.uber.org/fx"

func Module() fx.Option {
	return fx.Module("approval", fx.Provide(New))
}
