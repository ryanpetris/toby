package approvals

// Fx wiring for the process-wide approvals capability service.

import (
	"go.uber.org/fx"
)

// Module provides the approvals capability service.
func Module() fx.Option {
	return fx.Module("hostaction.methods.approvals",
		fx.Provide(New),
	)
}
