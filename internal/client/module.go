// fx wiring for the client. The caller includes a transport module that provides
// transport.Bootstrap and transport.Connector.

package client

import (
	"petris.dev/toby/internal/daemon/transport"

	"go.uber.org/fx"
)

// Module provides the client Service.
func Module() fx.Option {
	return fx.Module("client",
		fx.Provide(newService),
	)
}

// New builds a client over an explicit transport (used by tests and the CLI paths
// that construct the transport directly rather than through fx).
func New(bootstrap transport.Bootstrap, connector transport.Connector) *Service {
	return newService(bootstrap, connector)
}
