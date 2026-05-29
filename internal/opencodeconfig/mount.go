package opencodeconfig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/configfile"
	"petris.dev/toby/internal/contextfiles"
	"petris.dev/toby/internal/openai"
	"petris.dev/toby/internal/tobyconfig"
)

const (
	StaticGitignorePath = "opencode/.gitignore"
	StaticConfigPath    = "opencode/opencode.json"
)

var opencodeGitignore = []byte("*\n")

var substitutionPattern = regexp.MustCompile(`\{(env|file):([^}]+)\}`)

type Mount struct {
	projectRoot   string
	instructions  []string
	tobyConfig    *tobyconfig.Service
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

func (r *Renderer) newMount(projectRoot string, instructions []string, cfg *tobyconfig.Service) (*Mount, error) {
	if r == nil || r.http == nil {
		return nil, errors.New("opencode renderer requires an HTTP client")
	}
	return &Mount{projectRoot: projectRoot, instructions: append([]string(nil), instructions...), tobyConfig: cfg, http: r.http}, nil
}

func (r *Renderer) RegisterContextFiles(ctx context.Context, registrar contextfiles.Registrar, projectRoot string, instructions []string, cfg *tobyconfig.Service) ([]error, error) {
	mount, err := r.newMount(projectRoot, instructions, cfg)
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
		configfile.Merge(config, m.tobySyntheticConfig())
	}
	m.populateProviderModels(ctx, config)
	addSynthetic(config, m.projectRoot, m.instructions)
	return marshalConfig(config)
}

func (m *Mount) tobySyntheticConfig() map[string]any {
	config := map[string]any{}
	mcp := map[string]any{}
	for name, server := range m.tobyConfig.MCPServers() {
		mcp[name] = server.Raw()
	}
	if len(mcp) > 0 {
		config["mcp"] = mcp
	}
	permission := m.tobyConfig.Permission()
	if len(permission.ExternalDirectory) > 0 {
		external := map[string]any{}
		for pattern, mode := range permission.ExternalDirectory {
			external[pattern] = mode
		}
		config["permission"] = map[string]any{"external_directory": external}
	}
	providers := map[string]any{}
	for name, provider := range m.tobyConfig.Providers() {
		providers[name] = provider.Raw()
	}
	if len(providers) > 0 {
		config["provider"] = providers
	}
	return config
}

func marshalConfig(config map[string]any) ([]byte, error) {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func (m *Mount) populateProviderModels(ctx context.Context, config map[string]any) {
	providers, ok := config["provider"].(map[string]any)
	if !ok {
		return
	}
	for providerID, rawProvider := range providers {
		provider, ok := rawProvider.(map[string]any)
		if !ok {
			continue
		}
		if _, specified := provider["models"]; specified {
			continue
		}
		if !isOpenAICompatibleProvider(provider) {
			continue
		}
		modelIDs, err := m.fetchProviderModelIDs(ctx, providerID, provider)
		if err != nil {
			m.modelWarnings = append(m.modelWarnings, fmt.Errorf("fetch OpenCode models for provider %q: %w", providerID, err))
			delete(providers, providerID)
			continue
		}
		provider["models"] = providerModels(modelIDs)
	}
}

func isOpenAICompatibleProvider(provider map[string]any) bool {
	if provider["npm"] != "@ai-sdk/openai-compatible" {
		return false
	}
	options, ok := provider["options"].(map[string]any)
	if !ok {
		return false
	}
	baseURL, ok := options["baseURL"].(string)
	return ok && strings.TrimSpace(baseURL) != ""
}

func (m *Mount) fetchProviderModelIDs(ctx context.Context, providerID string, provider map[string]any) ([]string, error) {
	options := provider["options"].(map[string]any)
	baseURL := strings.TrimSpace(options["baseURL"].(string))
	headers, err := resolveHeaders(providerID, options, m.substitutionDirs())
	if err != nil {
		return nil, err
	}
	token := ""
	if auth := headers["Authorization"]; strings.HasPrefix(auth, "Bearer ") {
		token = strings.TrimPrefix(auth, "Bearer ")
		delete(headers, "Authorization")
	}
	client, err := openai.NewClient(m.http, baseURL, token, headers)
	if err != nil {
		return nil, err
	}
	return client.ModelIDs(ctx)
}

func (m *Mount) substitutionDirs() []string {
	if m.tobyConfig != nil && m.tobyConfig.Dir != "" {
		return []string{m.tobyConfig.Dir}
	}
	return nil
}

func resolveHeaders(providerID string, options map[string]any, configDirs []string) (map[string]string, error) {
	headers := map[string]string{}
	rawHeaders := options["headers"]
	if rawHeaders == nil {
		rawHeaders = map[string]any{}
	}
	headerMap, ok := rawHeaders.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("provider %q has non-object options.headers", providerID)
	}
	for key, rawValue := range headerMap {
		value, ok := rawValue.(string)
		if !ok {
			return nil, fmt.Errorf("provider %q has non-string header values", providerID)
		}
		resolved, err := resolveString(value, configDirs)
		if err != nil {
			return nil, err
		}
		headers[key] = resolved
	}
	if rawAPIKey, exists := options["apiKey"]; exists && rawAPIKey != nil {
		apiKey, ok := rawAPIKey.(string)
		if !ok {
			return nil, fmt.Errorf("provider %q has non-string options.apiKey", providerID)
		}
		resolved, err := resolveString(apiKey, configDirs)
		if err != nil {
			return nil, err
		}
		if resolved != "" {
			if _, exists := headers["Authorization"]; !exists {
				headers["Authorization"] = "Bearer " + resolved
			}
		}
	}
	return headers, nil
}

func resolveString(value string, configDirs []string) (string, error) {
	var firstErr error
	resolved := substitutionPattern.ReplaceAllStringFunc(value, func(match string) string {
		if firstErr != nil {
			return ""
		}
		parts := substitutionPattern.FindStringSubmatch(match)
		kind := parts[1]
		target := strings.TrimSpace(parts[2])
		if kind == "env" {
			return os.Getenv(target)
		}
		path := config.ExpandHome(target, homeDir())
		data, err := readSubstitutionFile(path, configDirs)
		if err != nil {
			firstErr = fmt.Errorf("unable to read file substitution %q: %w", target, err)
			return ""
		}
		return strings.TrimSpace(string(data))
	})
	if firstErr != nil {
		return "", firstErr
	}
	return resolved, nil
}

func readSubstitutionFile(path string, configDirs []string) ([]byte, error) {
	if filepath.IsAbs(path) {
		return os.ReadFile(path)
	}
	var firstErr error
	for _, dir := range configDirs {
		if dir == "" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, path))
		if err == nil {
			return data, nil
		}
		if firstErr == nil {
			firstErr = err
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return os.ReadFile(path)
}

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

func providerModels(modelIDs []string) map[string]any {
	models := make(map[string]any, len(modelIDs))
	for _, id := range modelIDs {
		models[id] = map[string]any{"name": id}
	}
	return models
}

func addSynthetic(config map[string]any, projectRoot string, instructions []string) {
	mcp := objectAt(config, "mcp")
	mcp["toby"] = syntheticMCP()
	addInstructions(config, instructions)
	permission := objectAt(config, "permission")
	external, ok := permission["external_directory"].(map[string]any)
	if !ok {
		external = map[string]any{}
		permission["external_directory"] = external
	}
	for _, pattern := range allowedExternalDirectoryPatterns(projectRoot) {
		external[pattern] = "allow"
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

func syntheticMCP() map[string]any {
	return map[string]any{
		"type":    "local",
		"command": []any{"toby", "sandbox", "mcp"},
		"enabled": true,
	}
}

func allowedExternalDirectoryPatterns(projectRoot string) []string {
	return []string{"/tmp", "/tmp/**", projectRoot, filepath.Join(projectRoot, "**")}
}
