package anthropic

// fx wiring: registers the Service into the "providers" group as a providers.Client.

import (
	"net/http"

	"go.uber.org/fx"

	"petris.dev/toby/providers"
)

// New constructs the Service with the shared HTTP client.
func New(httpClient *http.Client) *Service {
	return &Service{http: httpClient}
}

// Module registers the Service into the providers group as a providers.Client.
func Module() fx.Option {
	return fx.Module("providers.anthropic",
		fx.Provide(
			fx.Annotate(New, fx.As(new(providers.Client)), fx.ResultTags(`group:"providers"`)),
		),
	)
}
