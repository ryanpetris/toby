// fx wiring for the WebSocket transport. Same shape as the unix-socket module: one
// value bound to all three transport interfaces. The composition root supplies the
// Config (listen address from settings.daemon.websocket.address).

package websocket

import (
	"petris.dev/toby/internal/daemon/transport"

	"go.uber.org/fx"
)

// Module provides the WebSocket transport bound to all three transport interfaces.
// It supplies a default Config; the composition root overrides the address by
// decorating Config from settings.daemon.websocket.address.
func Module() fx.Option {
	return fx.Module("transport.websocket",
		fx.Supply(Config{}),
		fx.Provide(
			fx.Annotate(
				New,
				fx.As(new(transport.Listener)),
				fx.As(new(transport.Connector)),
				fx.As(new(transport.Bootstrap)),
			),
		),
	)
}

// New builds the WebSocket transport for the given config.
func New(cfg Config) *Service {
	return newService(cfg)
}
