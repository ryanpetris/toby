package git

// Fx wiring for the process-wide Git capability service.

import (
	"go.uber.org/fx"
)

// Module provides the Git capability service.
func Module() fx.Option {
	return fx.Module("hostaction.methods.git",
		fx.Provide(New),
	)
}
