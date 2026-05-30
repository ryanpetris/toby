package hostmanager

import (
	"petris.dev/toby/internal/mcpproxy"

	"go.uber.org/fx"
)

func Module() fx.Option {
	return fx.Module(
		"hostmanager",
		fx.Provide(
			mcpproxy.NewService,
			NewContextService,
			NewCommandService,
			NewGitService,
			NewRegistry,
			NewHostManager,
		),
	)
}
