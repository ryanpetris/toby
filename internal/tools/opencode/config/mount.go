package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"petris.dev/toby/internal/config/file"
	"petris.dev/toby/internal/config/toby"
	"petris.dev/toby/internal/context/files"
	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/control/httpproxy"
	"petris.dev/toby/internal/control/mcpproxy"
	"petris.dev/toby/internal/providers/anthropic"
	"petris.dev/toby/internal/providers/openai"
	sandboxpath "petris.dev/toby/internal/sandbox/path"
	"petris.dev/toby/internal/tools/toolconfig/proxyconfig"
)

const (
	StaticGitignorePath = "opencode/.gitignore"
	StaticConfigPath    = "opencode/opencode.json"
)

var opencodeGitignore = []byte("*\n")

type Mount struct {
	paths         sandboxpath.Paths
	controlHost   string
	tobyMCPURL    string
	instructions  []string
	tobyConfig    *tobyconfig.Service
	proxy         *httpproxy.Service
	mcpProxy      *mcpproxy.Service
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

func (r *Renderer) newMount(paths sandboxpath.Paths, controlHost, tobyMCPURL string, instructions []string, cfg *tobyconfig.Service, proxy *httpproxy.Service, mcpProxy *mcpproxy.Service) (*Mount, error) {
	if r == nil || r.http == nil {
		return nil, errors.New("opencode renderer requires an HTTP client")
	}
	return &Mount{paths: paths, controlHost: controlHost, tobyMCPURL: tobyMCPURL, instructions: append([]string(nil), instructions...), tobyConfig: cfg, proxy: proxy, mcpProxy: mcpProxy, http: r.http}, nil
}

func (r *Renderer) RegisterContextFiles(ctx context.Context, registrar contextfiles.Registrar, paths sandboxpath.Paths, controlHost, tobyMCPURL string, instructions []string, cfg *tobyconfig.Service, proxy *httpproxy.Service, mcpProxy *mcpproxy.Service) ([]error, error) {
	mount, err := r.newMount(paths, controlHost, tobyMCPURL, instructions, cfg, proxy, mcpProxy)
	if err != nil {
		return nil, err
	}
	config, err := mount.render(ctx)
	if err != nil {
		return nil, err
	}
	if err := registrar.AddBytes(StaticGitignorePath, opencodeGitignore, 0o644); err != nil {
		return nil, err
	}
	if err := registrar.AddBytes(StaticConfigPath, config, 0o644); err != nil {
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
		headers, err := m.tobyConfig.ResolveProviderHeaders(providerID, provider)
		if err != nil {
			return nil, err
		}
		client, err := anthropic.NewClient(m.http, provider.BaseURL, headerStrings(headers))
		if err != nil {
			return nil, err
		}
		models, err := client.Models(ctx)
		if err != nil {
			return nil, err
		}
		return anthropicProviderModels(models), nil
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

func anthropicProviderModels(modelList []anthropic.Model) map[string]any {
	models := make(map[string]any, len(modelList))
	for _, model := range modelList {
		name := model.ID
		if model.DisplayName != "" {
			name = model.DisplayName
		}
		models[model.ID] = map[string]any{"name": name}
	}
	return models
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
	addPermissionPaths(config, m.tobyConfig.PermissionPaths(m.paths))
	return nil
}

func addPermissionPaths(config map[string]any, paths map[string]string) {
	if len(paths) == 0 {
		return
	}
	permission := objectAt(config, "permission")
	external, ok := permission["external_directory"].(map[string]any)
	if !ok {
		external = map[string]any{}
		permission["external_directory"] = external
	}
	for pattern, mode := range paths {
		external[pattern] = mode
	}
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
	proxyURL, err := proxyconfig.MCPURL(m.controlHost, m.proxy, m.mcpProxy, name, server)
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
