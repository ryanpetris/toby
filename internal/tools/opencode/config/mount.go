package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"petris.dev/toby/internal/config/file"
	"petris.dev/toby/internal/config/toby"
	"petris.dev/toby/internal/context/files"
	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/control/httpproxy"
	"petris.dev/toby/internal/providers/openai"
	"petris.dev/toby/internal/tools/toolconfig/proxyconfig"
)

const (
	StaticGitignorePath = "opencode/.gitignore"
	StaticConfigPath    = "opencode/opencode.json"
)

var opencodeGitignore = []byte("*\n")

type Mount struct {
	projectRoot   string
	controlHost   string
	tobyMCPURL    string
	instructions  []string
	tobyConfig    *tobyconfig.Service
	proxy         *httpproxy.Service
	http          *http.Client
	modelWarnings []error
}

type Renderer struct {
	http *http.Client
}

func NewRenderer(client *http.Client) (*Renderer, error) {
	if client == nil {
		return nil, errors.New("opencode renderer requires an HTTP client")
	}
	return &Renderer{http: client}, nil
}

func (r *Renderer) newMount(projectRoot, controlHost, tobyMCPURL string, instructions []string, cfg *tobyconfig.Service, proxy *httpproxy.Service) (*Mount, error) {
	if r == nil || r.http == nil {
		return nil, errors.New("opencode renderer requires an HTTP client")
	}
	return &Mount{projectRoot: projectRoot, controlHost: controlHost, tobyMCPURL: tobyMCPURL, instructions: append([]string(nil), instructions...), tobyConfig: cfg, proxy: proxy, http: r.http}, nil
}

func (r *Renderer) RegisterContextFiles(ctx context.Context, registrar contextfiles.Registrar, projectRoot, controlHost, tobyMCPURL string, instructions []string, cfg *tobyconfig.Service, proxy *httpproxy.Service) ([]error, error) {
	mount, err := r.newMount(projectRoot, controlHost, tobyMCPURL, instructions, cfg, proxy)
	if err != nil {
		return nil, err
	}
	config, err := mount.render(ctx)
	if err != nil {
		return nil, err
	}
	if err := registrar.AddBytes(StaticGitignorePath, opencodeGitignore, 0o400); err != nil {
		return nil, err
	}
	if err := registrar.AddBytes(StaticConfigPath, config, 0o400); err != nil {
		return nil, err
	}
	return append([]error(nil), mount.modelWarnings...), nil
}

func (m *Mount) render(ctx context.Context) ([]byte, error) {
	config := map[string]any{"$schema": "https://opencode.ai/config.json"}
	if m.tobyConfig != nil {
		synthetic, err := m.tobySyntheticConfig(ctx)
		if err != nil {
			return nil, err
		}
		configfile.Merge(config, synthetic)
	}
	if err := m.addSynthetic(config); err != nil {
		return nil, err
	}
	return marshalConfig(config)
}

func (m *Mount) tobySyntheticConfig(ctx context.Context) (map[string]any, error) {
	config := map[string]any{}
	mcp := map[string]any{}
	for name, server := range m.tobyConfig.MCPServers() {
		if !server.Enabled() {
			continue
		}
		if server.HTTPProxyable() {
			converted, err := m.syntheticProxyMCP(name, server)
			if err != nil {
				return nil, err
			}
			mcp[name] = converted
			continue
		}
		mcp[name] = server.Raw()
	}
	if len(mcp) > 0 {
		config["mcp"] = mcp
	}
	permission := m.tobyConfig.Permission()
	if len(permission.Paths) > 0 {
		external := map[string]any{}
		for pattern, mode := range permission.Paths {
			external[pattern] = mode
		}
		config["permission"] = map[string]any{"external_directory": external}
	}
	providers := map[string]any{}
	for name, provider := range m.tobyConfig.Providers() {
		converted, err := m.syntheticProvider(ctx, name, provider)
		if err != nil {
			return nil, err
		}
		if converted != nil {
			providers[name] = converted
		}
	}
	if len(providers) > 0 {
		config["provider"] = providers
	}
	return config, nil
}

func marshalConfig(config map[string]any) ([]byte, error) {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func (m *Mount) syntheticProvider(ctx context.Context, providerID string, provider tobyconfig.ProviderConfig) (map[string]any, error) {
	if provider.Type != tobyconfig.ProviderTypeAnthropic && provider.Type != tobyconfig.ProviderTypeOpenAI {
		return nil, nil
	}
	if m.controlHost == "" {
		return nil, fmt.Errorf("provider %q requires %s", providerID, control.EnvControlHost)
	}
	if m.proxy == nil {
		return nil, fmt.Errorf("provider %q requires http proxy service", providerID)
	}
	headers, err := m.tobyConfig.ResolveProviderHeaders(providerID, provider)
	if err != nil {
		return nil, err
	}
	proxyID, err := m.proxy.Register(httpproxy.Target{BaseURL: provider.BaseURL, Headers: headers})
	if err != nil {
		return nil, fmt.Errorf("register provider %q proxy: %w", providerID, err)
	}
	proxyURL := control.Endpoint{Host: m.controlHost}.ProxyBaseURL(proxyID)
	converted := map[string]any{
		"options": map[string]any{
			"baseURL": proxyURL,
		},
	}
	if provider.Type == tobyconfig.ProviderTypeOpenAI {
		converted["npm"] = "@ai-sdk/openai-compatible"
	} else {
		converted["npm"] = "@ai-sdk/anthropic"
	}
	if provider.Name != "" {
		converted["name"] = provider.Name
	}
	if provider.HasModels() {
		converted["models"] = configfile.CloneMap(provider.Models)
		return converted, nil
	}
	models, err := m.fetchProviderModels(ctx, providerID, provider)
	if err != nil {
		m.modelWarnings = append(m.modelWarnings, fmt.Errorf("fetch OpenCode models for provider %q: %w", providerID, err))
		return nil, nil
	}
	converted["models"] = models
	return converted, nil
}

func (m *Mount) fetchProviderModels(ctx context.Context, providerID string, provider tobyconfig.ProviderConfig) (map[string]any, error) {
	if provider.Type == tobyconfig.ProviderTypeAnthropic {
		return m.fetchAnthropicProviderModels(ctx, providerID, provider)
	}
	headers, err := m.tobyConfig.ResolveProviderHeaders(providerID, provider)
	if err != nil {
		return nil, err
	}
	client, err := openai.NewClient(m.http, provider.BaseURL, "", headerStrings(headers))
	if err != nil {
		return nil, err
	}
	ids, err := client.ModelIDs(ctx)
	if err != nil {
		return nil, err
	}
	return providerModels(ids), nil
}

func (m *Mount) fetchAnthropicProviderModels(ctx context.Context, providerID string, provider tobyconfig.ProviderConfig) (map[string]any, error) {
	headers, err := m.tobyConfig.ResolveProviderHeaders(providerID, provider)
	if err != nil {
		return nil, err
	}
	models := map[string]any{}
	var after string
	for {
		endpoint, err := anthropicModelsURL(provider.BaseURL, after)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", openai.UserAgent)
		for key, values := range headers {
			req.Header.Del(key)
			for _, value := range values {
				req.Header.Add(key, value)
			}
		}
		resp, err := m.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("request failed: %w", err)
		}
		payload, err := decodeAnthropicModelsResponse(resp)
		if err != nil {
			return nil, err
		}
		for _, model := range payload.Data {
			if model.ID == "" {
				continue
			}
			entry := map[string]any{"name": model.ID}
			if model.DisplayName != "" {
				entry["name"] = model.DisplayName
			}
			models[model.ID] = entry
		}
		if !payload.HasMore || payload.LastID == "" || payload.LastID == after {
			return models, nil
		}
		after = payload.LastID
	}
}

type anthropicModelsResponse struct {
	Data []struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	} `json:"data"`
	HasMore bool   `json:"has_more"`
	LastID  string `json:"last_id"`
}

func anthropicModelsURL(baseURL, after string) (string, error) {
	parsed, err := url.Parse(baseURL)
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

func decodeAnthropicModelsResponse(resp *http.Response) (anthropicModelsResponse, error) {
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		details := strings.TrimSpace(string(body))
		if details == "" {
			details = resp.Status
		}
		return anthropicModelsResponse{}, fmt.Errorf("request failed with HTTP %d: %s", resp.StatusCode, details)
	}
	var payload anthropicModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return anthropicModelsResponse{}, err
	}
	return payload, nil
}

func headerStrings(headers http.Header) map[string]string {
	items := make(map[string]string, len(headers))
	for key, values := range headers {
		if len(values) == 0 {
			continue
		}
		if len(values) == 1 {
			items[key] = values[0]
			continue
		}
		items[key] = strings.Join(values, ",")
	}
	return items
}

func providerModels(modelIDs []string) map[string]any {
	models := make(map[string]any, len(modelIDs))
	for _, id := range modelIDs {
		models[id] = map[string]any{"name": id}
	}
	return models
}

func (m *Mount) addSynthetic(config map[string]any) error {
	mcp := objectAt(config, "mcp")
	toby, err := syntheticMCP(m.tobyMCPURL)
	if err != nil {
		return err
	}
	mcp["toby"] = toby
	addInstructions(config, m.instructions)
	return nil
}

func addInstructions(config map[string]any, paths []string) {
	if len(paths) == 0 {
		return
	}
	instructions, ok := config["instructions"].([]any)
	if !ok {
		instructions = []any{}
	}
	seen := map[string]bool{}
	for _, item := range instructions {
		if path, ok := item.(string); ok {
			seen[path] = true
		}
	}
	for _, path := range paths {
		if path == "" || seen[path] {
			continue
		}
		instructions = append(instructions, path)
		seen[path] = true
	}
	config["instructions"] = instructions
}

func objectAt(config map[string]any, key string) map[string]any {
	if value, ok := config[key].(map[string]any); ok {
		return value
	}
	value := map[string]any{}
	config[key] = value
	return value
}

func syntheticMCP(url string) (map[string]any, error) {
	if strings.TrimSpace(url) == "" {
		return nil, fmt.Errorf("toby MCP proxy URL is required")
	}
	return map[string]any{
		"type":    "remote",
		"url":     strings.TrimSpace(url),
		"enabled": true,
	}, nil
}

func (m *Mount) syntheticProxyMCP(name string, server tobyconfig.MCPServer) (map[string]any, error) {
	proxyURL, err := proxyconfig.MCPURL(m.controlHost, m.proxy, server)
	if err != nil {
		return nil, fmt.Errorf("mcp.%s: %w", name, err)
	}
	converted := map[string]any{
		"type":    "remote",
		"url":     proxyURL,
		"enabled": true,
	}
	raw := server.Raw()
	for _, key := range []string{"tools"} {
		if value, ok := raw[key]; ok {
			converted[key] = configfile.Clone(value)
		}
	}
	return converted, nil
}
