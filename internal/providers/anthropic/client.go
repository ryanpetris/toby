package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const UserAgent = "petris-toby/1"

type Client struct {
	baseURL string
	headers map[string]string
	http    *http.Client
}

type Model struct {
	ID          string
	DisplayName string
}

func NewClient(httpClient *http.Client, baseURL string, headers map[string]string) (Client, error) {
	if httpClient == nil {
		return Client{}, fmt.Errorf("anthropic client requires an HTTP client")
	}
	resolved := map[string]string{
		"Accept":     "application/json",
		"User-Agent": UserAgent,
	}
	for key, value := range headers {
		resolved[key] = value
	}
	return Client{baseURL: strings.TrimSpace(baseURL), headers: resolved, http: httpClient}, nil
}

func (c Client) Models(ctx context.Context) ([]Model, error) {
	models := []Model{}
	var after string
	for {
		endpoint, err := c.modelsURL(after)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		for key, value := range c.headers {
			req.Header.Set(key, value)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("request failed: %w", err)
		}
		payload, err := decodeModelsResponse(resp)
		if err != nil {
			return nil, err
		}
		for _, model := range payload.Data {
			if model.ID == "" {
				continue
			}
			models = append(models, Model{ID: model.ID, DisplayName: model.DisplayName})
		}
		if !payload.HasMore || payload.LastID == "" || payload.LastID == after {
			return models, nil
		}
		after = payload.LastID
	}
}

func (c Client) modelsURL(after string) (string, error) {
	parsed, err := url.Parse(c.baseURL)
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

type modelsResponse struct {
	Data []struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	} `json:"data"`
	HasMore bool   `json:"has_more"`
	LastID  string `json:"last_id"`
}

func decodeModelsResponse(resp *http.Response) (modelsResponse, error) {
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		details := strings.TrimSpace(string(body))
		if details == "" {
			details = resp.Status
		}
		return modelsResponse{}, fmt.Errorf("request failed with HTTP %d: %s", resp.StatusCode, details)
	}
	var payload modelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return modelsResponse{}, err
	}
	return payload, nil
}
