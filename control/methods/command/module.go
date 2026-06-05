package command

// fx wiring: the command Service is registered as a concrete *Service (so the
// sandbox runtime can set its Sender and drive signal/terminate) and into the
// sandbox handler group.

import (
	"go.uber.org/fx"

	"petris.dev/toby/control"
)

// Module provides the command Service and registers it as a sandbox-side capability.
func Module() fx.Option {
	return fx.Module("control.methods.command",
		fx.Provide(
			New,
			fx.Annotate(asCapability, fx.As(new(control.Capability)), fx.ResultTags(`group:"control.sandbox.handlers"`)),
		),
	)
}

func asCapability(s *Service) control.Capability { return s }
