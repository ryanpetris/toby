package grok

// Fx wiring for the Grok tool.

import (
	sessionconfig "petris.dev/toby/internal/config/session"
	"petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/tools"

	"go.uber.org/fx"
)

// params contains the tool constructor dependencies.
type params struct {
	fx.In

	SessionConfig *sessionconfig.Holder
	Sandbox       sandbox.Service
}

// result exposes the tool through Fx.
type result struct {
	fx.Out

	Service tools.Tool `group:"tools"`
}

// Module provides the package's components to Fx.
var Module = fx.Module("tools.grok", fx.Provide(provide))
