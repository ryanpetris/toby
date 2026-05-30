package sandbox

import "go.uber.org/fx"

func Module() fx.Option {
	return fx.Module(
		"sandbox",
		fx.Provide(
			ProvideBubblewrapEnvironment,
			ProvideDockerEnvironment,
			ProvideFactory,
		),
	)
}
