// Package anthropic implements the providers.Client for the Anthropic API,
// listing models from the paginated /models endpoint.
package anthropic

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/providers"
	"petris.dev/toby/internal/version"
)

// Service queries the Anthropic /models endpoint.
type Service struct {
	http   *http.Client
	logger *diagnostic.Logger
}

var _ providers.Client = (*Service)(nil)

// Kind reports the Anthropic provider kind.
func (s *Service) Kind() providers.Kind {
	return providers.KindAnthropic
}

// LookupModels lists the models offered by the Anthropic endpoint at baseURL,
// following pagination. Models without a display name fall back to their ID.
func (s *Service) LookupModels(ctx context.Context, baseURL string, headers map[string]string) ([]providers.Model, error) {
	if s.http == nil {
		return nil, fmt.Errorf("anthropic provider requires an HTTP client")
	}

	models := []providers.Model{}
	seenModels := make(map[string]struct{})
	seenCursors := make(map[string]struct{})
	var after string
	for page := 0; page < maxModelPages; page++ {
		endpoint, err := modelsURL(baseURL, after)
		if err != nil {
			return nil, fmt.Errorf(
				"anthropic model request is invalid",
			)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, fmt.Errorf(
				"anthropic model request is invalid",
			)
		}
		for key, value := range requestHeaders(headers) {
			req.Header.Set(key, value)
		}

		resp, err := s.http.Do(req)
		if err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return nil, contextErr
			}
			return nil, fmt.Errorf(
				"anthropic model request failed",
			)
		}

		payload, err := decodeModelsResponse(resp, s.logger)
		if err != nil {
			return nil, err
		}
		for _, model := range payload.Data {
			if model.ID == "" {
				continue
			}
			if _, duplicate := seenModels[model.ID]; duplicate {
				continue
			}
			seenModels[model.ID] = struct{}{}

			displayName := model.DisplayName
			if displayName == "" {
				displayName = model.ID
			}
			models = append(models, providers.Model{ID: model.ID, DisplayName: displayName})
		}
		if len(models) > maxModels {
			return nil, fmt.Errorf(
				"anthropic model response exceeds its model limit",
			)
		}

		if !payload.HasMore {
			return models, nil
		}
		if payload.LastID == "" {
			return nil, fmt.Errorf(
				"anthropic model response has an invalid cursor",
			)
		}
		if _, duplicate := seenCursors[payload.LastID]; duplicate {
			return nil, fmt.Errorf(
				"anthropic model response repeated a cursor",
			)
		}
		seenCursors[payload.LastID] = struct{}{}
		after = payload.LastID
	}

	return nil, fmt.Errorf(
		"anthropic model response exceeds its page limit",
	)
}

func modelsURL(baseURL, after string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return "", err
	}

	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/models"
	if after != "" {
		query := parsed.Query()
		query.Set("after_id", after)
		parsed.RawQuery = query.Encode()
	}

	return parsed.String(), nil
}

func requestHeaders(headers map[string]string) map[string]string {
	resolved := map[string]string{
		"Accept":     "application/json",
		"User-Agent": version.UserAgent,
	}
	for key, value := range headers {
		resolved[key] = value
	}

	return resolved
}
