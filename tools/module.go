package tools

// fx wiring: the Registry, consuming every Tool registered into the "toby.tools"
// group by the per-tool subpackages.

import "go.uber.org/fx"

// Module provides the Registry, consuming every Tool in the "toby.tools" group.
func Module() fx.Option {
	return fx.Module("tools",
		fx.Provide(
			fx.Annotate(NewRegistry, fx.ParamTags(`group:"toby.tools"`)),
		),
	)
}
