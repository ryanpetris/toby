package hostmanager

import "go.uber.org/fx"

func Module() fx.Option {
	return fx.Module(
		"hostmanager",
		fx.Provide(
			NewContextService,
			NewCommandService,
			NewGitService,
			NewRegistry,
			NewHostManager,
		),
	)
}
