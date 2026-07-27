package providergateway

// Declares the protected agent request and sandbox-safe provider descriptor
// contracts.

import (
	"fmt"

	configfile "petris.dev/toby/internal/config/file"
)

const (
	// ResourceKind is the background resource kind for one models gateway
	// generation.
	ResourceKind = "models-gateway"
)

// ProviderType identifies the provider API shape exposed by a route.
type ProviderType string

const (
	// ProviderOpenAI selects the OpenAI-compatible models protocol.
	ProviderOpenAI ProviderType = "openai"
	// ProviderAnthropic selects the Anthropic models protocol.
	ProviderAnthropic ProviderType = "anthropic"
)

// RequestSpec is the complete secret-bearing provider set sent over the
// authenticated agent connection.
type RequestSpec struct {
	Providers []ProviderSpec `json:"providers"`
}

// ProviderSpec is one host-resolved provider definition. URL and Headers are
// protected host values and must never enter a returned descriptor.
type ProviderSpec struct {
	ID      string            `json:"id"`
	Type    ProviderType      `json:"type"`
	Name    string            `json:"name"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

var _ fmt.Stringer = ProviderSpec{}

// String exposes only non-secret identity metadata.
func (s ProviderSpec) String() string {
	return fmt.Sprintf(
		"{ID:%q Type:%q Name:%q Upstream:<redacted>}",
		s.ID,
		s.Type,
		s.Name,
	)
}

// DescriptorConfig is the agent-private provider capability returned after
// all requested routes are ready.
type DescriptorConfig struct {
	Providers []ProviderDescriptor `json:"providers"`
}

var _ fmt.Stringer = DescriptorConfig{}

// String withholds capability URLs, synthetic credentials, and provider IDs.
func (c DescriptorConfig) String() string {
	return fmt.Sprintf(
		"{Providers:<redacted count=%d>}",
		len(c.Providers),
	)
}

// ProviderDescriptor is one sandbox-visible route. Credential is synthetic
// capability material, never an upstream provider secret.
type ProviderDescriptor struct {
	ID         string         `json:"id"`
	Type       ProviderType   `json:"type"`
	Name       string         `json:"name"`
	URL        string         `json:"url"`
	Credential string         `json:"credential"`
	Models     map[string]any `json:"models"`
}

var _ fmt.Stringer = ProviderDescriptor{}

// String exposes only display metadata.
func (d ProviderDescriptor) String() string {
	return fmt.Sprintf(
		"{ID:%q Type:%q Name:%q Capability:<redacted>}",
		d.ID,
		d.Type,
		d.Name,
	)
}

func (s ProviderSpec) clone() ProviderSpec {
	clone := s
	clone.Headers = cloneStringMap(s.Headers)

	return clone
}

func (c DescriptorConfig) clone() DescriptorConfig {
	clone := DescriptorConfig{
		Providers: make([]ProviderDescriptor, len(c.Providers)),
	}
	for index, provider := range c.Providers {
		clone.Providers[index] = provider.clone()
	}

	return clone
}

func (d ProviderDescriptor) clone() ProviderDescriptor {
	clone := d
	clone.Models = configfile.CloneMap(d.Models)

	return clone
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}

	clone := make(map[string]string, len(source))
	for name, value := range source {
		clone[name] = value
	}

	return clone
}
