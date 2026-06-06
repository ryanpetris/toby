package git

// fx wiring: the git Service is registered as a concrete *Service (so the session
// can install its repository resolver) and into the host handler group.

import (
	"go.uber.org/fx"

	"petris.dev/toby/internal/control"
)

// Module provides the git Service and registers it as a host-side capability.
func Module() fx.Option {
	return fx.Module("control.methods.git",
		fx.Provide(
			New,
			fx.Annotate(asCapability, fx.As(new(control.Capability)), fx.ResultTags(`group:"control.host.handlers"`)),
		),
	)
}

func asCapability(s *Service) control.Capability { return s }
