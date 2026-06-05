package files

// fx wiring: registers the file capability into the sandbox handler group.

import (
	"go.uber.org/fx"

	"petris.dev/toby/control"
)

// Module registers the file capability as a sandbox-side control handler.
func Module() fx.Option {
	return fx.Module("control.methods.files",
		fx.Provide(
			fx.Annotate(New, fx.As(new(control.Capability)), fx.ResultTags(`group:"control.sandbox.handlers"`)),
		),
	)
}
