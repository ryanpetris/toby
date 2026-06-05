package env

// fx wiring: the env Service is registered both as a concrete *Service (for
// capabilities that read the environment) and into the sandbox handler group.

import (
	"go.uber.org/fx"

	"petris.dev/toby/control"
)

// Module provides the env Service and registers it as a sandbox-side capability.
func Module() fx.Option {
	return fx.Module("control.methods.env",
		fx.Provide(
			New,
			fx.Annotate(asCapability, fx.As(new(control.Capability)), fx.ResultTags(`group:"control.sandbox.handlers"`)),
		),
	)
}

func asCapability(s *Service) control.Capability { return s }
