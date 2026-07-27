package npm

// Fx wiring for the npm tool.

import (
	"petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/tools"

	"go.uber.org/fx"
)

// result exposes the tool through Fx.
type result struct {
	fx.Out

	Service tools.Tool `group:"tools"`
}

// params contains the tool constructor dependencies.
type params struct {
	fx.In

	Sandbox sandbox.Service
}

// Module provides the package's components to Fx.
var Module = fx.Module("tools.npm", fx.Provide(provide))
