package exectool

// Fx wiring for the exec tool.

import (
	"petris.dev/toby/internal/tools"

	"go.uber.org/fx"
)

// result exposes the tool through Fx.
type result struct {
	fx.Out

	Service tools.Tool `group:"tools"`
}

// Module provides the package's components to Fx.
var Module = fx.Module("tools.exec", fx.Provide(provide))
