package sandboxmanager

import "go.uber.org/fx"

func Module() fx.Option {
	return fx.Module(
		"sandboxmanager",
		fx.Provide(
			NewFileService,
			NewCommandService,
			NewSandboxService,
			NewRegistry,
			NewRunner,
		),
	)
}
