package providergateway

// Builds, validates, and protocol-bounds sandbox-visible provider descriptors.

import (
	"encoding/json"
	"fmt"

	"petris.dev/toby/internal/agent/protocol"
	configfile "petris.dev/toby/internal/config/file"
)

func newProviderDescriptor(
	item route,
	baseURL string,
	models map[string]any,
) (ProviderDescriptor, error) {
	descriptor := ProviderDescriptor{
		ID:         item.Provider.ID,
		Type:       item.Provider.Type,
		Name:       item.Provider.Name,
		URL:        baseURL + item.routePrefix(),
		Credential: item.Credential,
		Models:     configfile.CloneMap(models),
	}
	if err := descriptor.validate(); err != nil {
		return ProviderDescriptor{}, err
	}

	return descriptor, nil
}

func descriptorConfigFromSlots(
	slots []*ProviderDescriptor,
) DescriptorConfig {
	providers := make([]ProviderDescriptor, 0, len(slots))
	for _, descriptor := range slots {
		if descriptor == nil {
			continue
		}
		providers = append(providers, descriptor.clone())
	}

	return DescriptorConfig{
		Providers: providers,
	}
}

func encodeProviderDescriptorConfig(
	config DescriptorConfig,
) ([]byte, error) {
	if err := config.validate(); err != nil {
		return nil, fmt.Errorf(
			"validate models gateway descriptor: %w",
			err,
		)
	}

	encoded, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("encode models gateway descriptor")
	}
	if err := protocol.ValidateConfigurationDocument(encoded); err != nil {
		return nil, fmt.Errorf(
			"validate models gateway descriptor protocol payload: %w",
			err,
		)
	}

	return encoded, nil
}
