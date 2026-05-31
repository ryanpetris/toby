package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const UserAgent = "petris-toby/1"

type Client struct {
	baseURL string
	token   string
	headers map[string]string
	http    *http.Client
}

func NewClient(httpClient *http.Client, baseURL, token string, headers map[string]string) (Client, error) {
	if httpClient == nil {
		return Client{}, fmt.Errorf("openai client requires an HTTP client")
	}
	resolved := map[string]string{
		"Accept":     "application/json",
		"User-Agent": UserAgent,
	}
	for key, value := range headers {
		resolved[key] = value
	}
	if token != "" {
		if _, exists := resolved["Authorization"]; !exists {
			resolved["Authorization"] = "Bearer " + token
		}
	}
	return Client{baseURL: strings.TrimSpace(baseURL), token: token, headers: resolved, http: httpClient}, nil
}

func (c Client) ModelIDs(ctx context.Context) ([]string, error) {
	url := strings.TrimRight(c.baseURL, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
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
	ids := make([]string, 0, len(payload.Data))
	seen := map[string]bool{}
	for _, item := range payload.Data {
		if item.ID == "" || seen[item.ID] {
			continue
		}
		seen[item.ID] = true
		ids = append(ids, item.ID)
	}
	return ids, nil
}
