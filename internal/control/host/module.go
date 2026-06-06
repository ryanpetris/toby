package host

// fx wiring for the host reverse proxy and its method capabilities.

import (
	"petris.dev/toby/internal/control/httpproxy"
	"petris.dev/toby/internal/control/methods/git"

	"go.uber.org/fx"
)

func Module() fx.Option {
	return fx.Module(
		"control.host",
		httpproxy.Module(),
		git.Module(),
		fx.Provide(
			fx.Annotate(
				NewService,
				fx.ParamTags(`group:"control.host.handlers"`, `optional:"true"`),
			),
		),
	)
}
