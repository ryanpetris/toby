package hostmanager

import (
	"petris.dev/toby/internal/control/httpproxy"

	"go.uber.org/fx"
)

func Module() fx.Option {
	return fx.Module(
		"hostmanager",
		fx.Provide(
			httpproxy.NewService,
			NewContextService,
			NewCommandService,
			NewGitService,
			NewRegistry,
			NewHostManager,
		),
	)
}
