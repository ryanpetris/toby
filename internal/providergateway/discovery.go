package providergateway

// Resolves model metadata through the same loopback capability route and
// synthetic credential delivered to the sandbox.

import (
	"context"
	"fmt"

	"petris.dev/toby/internal/providers"
)

// Discovery adapts the provider-client registry to gateway capability routes.
type Discovery struct {
	registry *providers.Registry
}

var _ ModelDiscoverer = (*Discovery)(nil)

// NewDiscovery constructs capability-route model discovery.
func NewDiscovery(registry *providers.Registry) (*Discovery, error) {
	if registry == nil {
		return nil, fmt.Errorf(
			"provider discovery registry is required",
		)
	}

	return &Discovery{registry: registry}, nil
}

// Discover performs one uncached lookup and converts it to the tool-facing
// model map.
func (d *Discovery) Discover(
	ctx context.Context,
	descriptor ProviderDescriptor,
) (map[string]any, error) {
	if d == nil || d.registry == nil {
		return nil, fmt.Errorf(
			"provider model discovery is not configured",
		)
	}
	if ctx == nil {
		return nil, fmt.Errorf(
			"provider model discovery context is nil",
		)
	}

	var kind providers.Kind
	var headers map[string]string
	switch descriptor.Type {
	case ProviderOpenAI:
		kind = providers.KindOpenAI
		headers = map[string]string{
			openAICredentialHeader: "Bearer " +
				descriptor.Credential,
		}
	case ProviderAnthropic:
		kind = providers.KindAnthropic
		headers = map[string]string{
			anthropicCredentialHeader: descriptor.Credential,
		}
	default:
		return nil, fmt.Errorf(
			"provider model discovery type is unsupported",
		)
	}

	models, err := d.registry.LookupModels(
		ctx,
		kind,
		descriptor.URL,
		headers,
	)
	if err != nil {
		return nil, fmt.Errorf("provider model discovery failed")
	}

	result := make(map[string]any, len(models))
	for _, model := range models {
		if model.ID == "" {
			continue
		}
		displayName := model.DisplayName
		if displayName == "" {
			displayName = model.ID
		}
		result[model.ID] = map[string]any{
			"name": displayName,
		}
	}

	return result, nil
}
