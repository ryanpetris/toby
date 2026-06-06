// Package openai implements the providers.Client for OpenAI-compatible
// endpoints, listing models from the upstream /models endpoint.
package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"petris.dev/toby/internal/version"
	"petris.dev/toby/providers"
)

// Service queries an OpenAI-compatible /models endpoint.
type Service struct {
	http *http.Client
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
		return nil, err
	}
	for key, value := range requestHeaders(headers) {
		req.Header.Set(key, value)
	}

	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		details := strings.TrimSpace(string(body))
		if details == "" {
			details = resp.Status
		}
		return nil, fmt.Errorf("request failed with HTTP %d: %s", resp.StatusCode, details)
	}

	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("returned invalid JSON from %s: %w", url, err)
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
