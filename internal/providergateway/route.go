package providergateway

// Defines one agent-only provider route and its provider-specific synthetic
// authentication contract.

import (
	"fmt"
	"net/http"
)

const (
	internalGatewayTokenHeader = "X-Toby-Gateway-Token"
	internalCapabilityHeader   = "X-Toby-Capability"

	openAICredentialHeader    = "Authorization"
	anthropicCredentialHeader = "X-Api-Key"
)

type route struct {
	ID         string
	Capability string
	Credential string
	Provider   ProviderSpec
}

func (r route) clone() route {
	clone := r
	clone.Provider = r.Provider.clone()

	return clone
}

func (r route) validate() error {
	if !validCapabilityToken(r.ID, maxCredentialBytes) {
		return fmt.Errorf("provider route ID is invalid")
	}
	if !validCapabilityToken(r.Capability, maxCredentialBytes) {
		return fmt.Errorf("provider route capability is invalid")
	}
	if !validCapabilityToken(r.Credential, maxCredentialBytes) {
		return fmt.Errorf("provider route credential is invalid")
	}
	if err := r.Provider.validate(); err != nil {
		return err
	}

	return nil
}

func (r route) credentialHeader() (string, string) {
	switch r.Provider.Type {
	case ProviderOpenAI:
		return openAICredentialHeader, "Bearer " + r.Credential
	case ProviderAnthropic:
		return anthropicCredentialHeader, r.Credential
	default:
		return "", ""
	}
}

func (r route) routePrefix() string {
	return "/" + r.Capability + "/" + r.Provider.ID
}

func (r route) authPath() string {
	return "/authorize/" + r.ID
}

func (r route) matchesCredential(header http.Header) bool {
	name, expected := r.credentialHeader()
	if name == "" {
		return false
	}

	values := header.Values(name)
	return len(values) == 1 &&
		constantTimeEqual(values[0], expected)
}
