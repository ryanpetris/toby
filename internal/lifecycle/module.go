package lifecycle

// fx wiring for the launch lifecycle.

import "go.uber.org/fx"

// Module provides the Runner, consuming every Hook in the "lifecycle" group.
func Module() fx.Option {
	return fx.Module("lifecycle",
		fx.Provide(
			fx.Annotate(NewRunner, fx.ParamTags(`group:"lifecycle"`)),
		),
	)
}
