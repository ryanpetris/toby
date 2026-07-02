package runtime

// fx wiring for the sandbox runtime: the tool-facing Service (bound to the
// sandbox.Service interface) and the Factory that resolves launch options into a Spec.

import (
	sandboxapi "petris.dev/toby/sandbox"

	"go.uber.org/fx"
)

// Module wires the sandbox runtime: the tool-facing Service (bound to the
// sandbox.Service interface) and the Factory that resolves launch options.
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
