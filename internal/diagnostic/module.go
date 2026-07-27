package diagnostic

// Fx wiring for process-wide diagnostic output.

import "go.uber.org/fx"

// Module provides the diagnostic service as a process singleton.
func Module() fx.Option {
	return fx.Module(
		"diagnostic",
		fx.Provide(
			OptionsFromEnvironment,
			NewService,
		),
	)
}
