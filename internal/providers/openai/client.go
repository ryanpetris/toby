// Package openai implements the providers.Client for OpenAI-compatible
// endpoints, listing models from the upstream /models endpoint.
package openai

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/providers"
	"petris.dev/toby/internal/version"
)

// Service queries an OpenAI-compatible /models endpoint.
type Service struct {
	http   *http.Client
	logger *diagnostic.Logger
}

var _ providers.Client = (*Service)(nil)

// Kind reports the OpenAI provider kind.
func (s *Service) Kind() providers.Kind {
	return providers.KindOpenAI
}

// LookupModels lists the models offered by the OpenAI-compatible endpoint at
// baseURL. The /models response carries no display name, so each model's
// DisplayName falls back to its ID.
func (s *Service) LookupModels(ctx context.Context, baseURL string, headers map[string]string) ([]providers.Model, error) {
	if s.http == nil {
		return nil, fmt.Errorf("openai provider requires an HTTP client")
	}

	url := strings.TrimRight(strings.TrimSpace(baseURL), "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf(
			"OpenAI model request is invalid",
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
		return nil, fmt.Errorf("OpenAI model request failed")
	}
	payload, err := decodeModelsResponse(resp, s.logger)
	if err != nil {
		return nil, err
	}

	models := make([]providers.Model, 0, len(payload.Data))
	seen := map[string]bool{}
	for _, item := range payload.Data {
		if item.ID == "" || seen[item.ID] {
			continue
		}
		seen[item.ID] = true
		models = append(models, providers.Model{ID: item.ID, DisplayName: item.ID})
	}

	return models, nil
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
