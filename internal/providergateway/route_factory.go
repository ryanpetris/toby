package providergateway

// Generates independent opaque route identities, path capabilities, and
// provider-compatible synthetic credentials.

import "fmt"

const routeGenerationAttempts = 8

func buildRoutes(
	spec RequestSpec,
	newToken func() (string, error),
) ([]route, error) {
	if err := spec.validate(); err != nil {
		return nil, err
	}
	if newToken == nil {
		return nil, fmt.Errorf(
			"models gateway token generator is required",
		)
	}

	routes := make([]route, len(spec.Providers))
	used := make(map[string]struct{}, len(routes)*3)
	for index, provider := range spec.Providers {
		values := make([]string, 3)
		for tokenIndex := range values {
			var token string
			for attempt := 0; attempt < routeGenerationAttempts; attempt++ {
				candidate, err := newToken()
				if err != nil {
					return nil, err
				}
				if !validCapabilityToken(
					candidate,
					maxCredentialBytes,
				) {
					return nil, fmt.Errorf(
						"generated provider capability is invalid",
					)
				}
				if _, duplicate := used[candidate]; duplicate {
					continue
				}

				token = candidate
				used[token] = struct{}{}
				break
			}
			if token == "" {
				return nil, fmt.Errorf(
					"generate unique provider capability",
				)
			}
			values[tokenIndex] = token
		}

		routes[index] = route{
			ID:         values[0],
			Capability: values[1],
			Credential: values[2],
			Provider:   provider.clone(),
		}
	}

	return routes, nil
}
