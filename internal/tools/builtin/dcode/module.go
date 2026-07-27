package dcode

// Fx wiring for the Deep Agents Code tool.

import (
	appconfig "petris.dev/toby/internal/config/app"
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
	Config        *appconfig.LaunchHolder
}

// result exposes the tool through Fx.
type result struct {
	fx.Out

	Service tools.Tool `group:"tools"`
}

// Module provides the package's components to Fx.
var Module = fx.Module("tools.dcode", fx.Provide(provide))
