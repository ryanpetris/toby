// Package providers models upstream AI API endpoints (e.g. anthropic, openai)
// that Toby queries for the models they offer. Each concrete provider lives in a
// subpackage and registers itself into the fx "providers" group as a Client; the
// Registry collects that group and dispatches by Kind.
package providers

import "context"

// Kind identifies the upstream API shape a provider speaks. It is intentionally
// independent of any config enum so this package stays free of config imports.
type Kind string

const (
	KindOpenAI    Kind = "openai"
	KindAnthropic Kind = "anthropic"
)

// Group is the fx group name every provider Client registers into.
const Group = "providers"

// Model is one model offered by a provider. DisplayName is always populated:
// providers that do not supply a human-readable name fall back to the ID.
type Model struct {
	ID          string
	DisplayName string
}

// Client queries one upstream provider API for its available models.
type Client interface {
	// Kind reports which provider API shape this client speaks.
	Kind() Kind
	// LookupModels fetches the models offered by the endpoint at baseURL. The
	// given headers are sent with the request (auth, custom headers, …). It
	// reaches the network on every call; callers that want memoization should go
	// through the Registry.
	LookupModels(ctx context.Context, baseURL string, headers map[string]string) ([]Model, error)
}
