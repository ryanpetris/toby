package sandbox

import (
	"petris.dev/toby/internal/tools/tool"

	"go.uber.org/fx"
)

func Module() fx.Option {
	return fx.Module(
		"sandbox",
		fx.Provide(
			newService,
			func(s *Service) tool.SandboxService { return s },
			provideFactory,
		),
	)
}
