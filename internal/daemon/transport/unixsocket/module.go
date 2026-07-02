// Package unixsocket is the unix-domain-socket transport for the client<->daemon
// channel. It owns everything socket-specific — the socket/lock path derivation
// under the XDG runtime dir, the flock-guarded bind, stale-socket cleanup, and the
// detached `toby daemon` spawn — so none of it leaks into the daemon, client, or
// protocol packages.
package unixsocket

import (
	"petris.dev/toby/config"
	"petris.dev/toby/internal/daemon/transport"

	"go.uber.org/fx"
)

// Module provides the unix-socket transport bound to all three transport interfaces.
// The daemon graph consumes the Listener; the client graph consumes Connector and
// Bootstrap. A graph that never resolves an interface simply never uses it.
func Module() fx.Option {
	return fx.Module("transport.unixsocket",
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

// New builds a transport rooted at the given runtime paths.
func New(paths config.Paths) *Service {
	return newService(paths)
}
