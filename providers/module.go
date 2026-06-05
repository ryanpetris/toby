package providers

// fx wiring: the Registry, consuming every Client registered into the "providers"
// group by the per-provider subpackages (openai, anthropic, …).

import "go.uber.org/fx"

// Module provides the Registry, consuming every Client in the "providers" group.
func Module() fx.Option {
	return fx.Module("providers",
		fx.Provide(
			fx.Annotate(NewRegistry, fx.ParamTags(`group:"providers"`)),
		),
	)
}
