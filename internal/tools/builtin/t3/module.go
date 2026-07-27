package t3

// Fx wiring for the T3 Code tool.

import (
	"petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/tools"

	"go.uber.org/fx"
)

// params contains the tool constructor dependencies.
type params struct {
	fx.In

	Sandbox sandbox.Service
}

// result exposes the tool through Fx.
type result struct {
	fx.Out

	Service tools.Tool `group:"tools"`
}

// Module provides the package's components to Fx.
var Module = fx.Module("tools.t3", fx.Provide(provide))
