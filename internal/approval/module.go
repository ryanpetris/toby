package approval

// fx wiring for the approval service.

import "go.uber.org/fx"

// Module provides the package's components to Fx.
func Module() fx.Option {
	return fx.Module("approval", fx.Provide(New))
}
