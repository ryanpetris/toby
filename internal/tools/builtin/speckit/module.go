package speckit

// Fx wiring for the Spec Kit tool.

import (
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/tools"
	"petris.dev/toby/internal/tools/kit"

	"go.uber.org/fx"
)

// params contains the tool constructor dependencies.
type params struct {
	fx.In

	HTTP        *kit.HTTPClient
	Diagnostics *diagnostic.Service
	Sandbox     sandbox.Service
}

// result exposes the tool through Fx.
type result struct {
	fx.Out

	Service tools.Tool `group:"tools"`
}

// Module provides the package's components to Fx.
var Module = fx.Module("tools.speckit", fx.Provide(provide))
