package sandbox

import (
	sandboxapi "petris.dev/toby/sandbox"

	"go.uber.org/fx"
)

func Module() fx.Option {
	return fx.Module(
		"sandbox",
		fx.Provide(
			newService,
			func(s *Service) sandboxapi.Service { return s },
			provideFactory,
		),
	)
}
