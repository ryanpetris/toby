package httpproxy

// fx wiring and constructor for the proxy Service.

import (
	"net/http"

	"go.uber.org/fx"
)

// NewService constructs the proxy Service, reusing the host HTTP transport when
// one is provided so outbound proxying shares the host's dialing config.
func NewService(upstream *http.Client) *Service {
	client := &http.Client{}
	if upstream != nil && upstream.Transport != nil {
		client.Transport = upstream.Transport
	}
	return &Service{targets: map[string]target{}, http: client}
}

// Module provides the proxy Service, reusing the host HTTP client when one is
// supplied.
func Module() fx.Option {
	return fx.Module("httpproxy",
		fx.Provide(
			fx.Annotate(NewService, fx.ParamTags(`optional:"true"`)),
		),
	)
}
