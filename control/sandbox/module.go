package sandbox

// fx wiring for the sandbox control endpoint and its method capabilities.

import (
	"go.uber.org/fx"

	"petris.dev/toby/control/methods/command"
	"petris.dev/toby/control/methods/env"
	"petris.dev/toby/control/methods/files"
)

func Module() fx.Option {
	return fx.Module(
		"control.sandbox",
		command.Module(),
		env.Module(),
		files.Module(),
		fx.Provide(
			NewRunner,
		),
	)
}
