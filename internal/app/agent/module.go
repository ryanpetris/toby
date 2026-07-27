package agent

// Fx wiring for the per-user agent composition root.

import (
	agentservice "petris.dev/toby/internal/agent"
	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/diagnostic/warning"
	mcpgatewaywiring "petris.dev/toby/internal/mcpgateway/wiring"
	providergatewaywiring "petris.dev/toby/internal/providergateway/wiring"
	"petris.dev/toby/internal/shutdown"

	"go.uber.org/fx"
)

func module() fx.Option {
	return fx.Options(
		fx.NopLogger,
		fx.StopTimeout(shutdown.ProcessFinalizationGrace),
		diagnostic.Module(),
		agentservice.ServerModule(),
		mcpgatewaywiring.Module(),
		providergatewaywiring.Module(),
		fx.Provide(
			config.NewPaths,
			newWarningService,
		),
	)
}

func newWarningService(
	diagnostics *diagnostic.Service,
) *warning.Service {
	return warning.NewService(
		diagnostics.Logger("warning"),
		nil,
	)
}
