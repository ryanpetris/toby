package runtime

// fx wiring for the sandbox runtime: the control-channel Service (bound to the
// tool-facing sandbox.Service interface) and the Factory that builds instances.

import (
	sandboxapi "petris.dev/toby/sandbox"

	"go.uber.org/fx"
)

// Module wires the sandbox runtime: the control-channel Service (bound to the
// tool-facing sandbox.Service interface) and the Factory that builds Docker
// instances from launch options.
func Module() fx.Option {
	return fx.Module(
		"sandbox.runtime",
		fx.Provide(
			newService,
			func(s *Service) sandboxapi.Service { return s },
			NewFactory,
		),
	)
}
