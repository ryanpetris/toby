package anthropic

// fx wiring: registers the Service into the "providers" group as a providers.Client.

import (
	"net/http"

	"go.uber.org/fx"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/providers"
)

func newService(
	httpClient *http.Client,
	diagnostics *diagnostic.Service,
) *Service {
	return &Service{
		http:   httpClient,
		logger: diagnostics.Logger("providers.anthropic"),
	}
}

// Module registers the Service into the providers group as a providers.Client.
func Module() fx.Option {
	return fx.Module("providers.anthropic",
		fx.Provide(
			fx.Annotate(newService, fx.As(new(providers.Client)), fx.ResultTags(`group:"providers"`)),
		),
	)
}
