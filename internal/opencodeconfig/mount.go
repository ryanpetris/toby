package opencodeconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/openai"
	"petris.dev/toby/internal/staticfiles"

	"gopkg.in/yaml.v3"
)

const (
	StaticGitignorePath = "opencode/.gitignore"
	StaticConfigPath    = "opencode/opencode.json"
	maxConfig           = 1 << 20
)

var opencodeGitignore = []byte("*\n")

type sourceFormat string

const (
	formatJSON sourceFormat = "json"
	formatYAML sourceFormat = "yaml"
)

type sourceFile struct {
	path   string
	format sourceFormat
}

var substitutionPattern = regexp.MustCompile(`\{(env|file):([^}]+)\}`)

type Mount struct {
	configDir     string
	projectRoot   string
	instructions  []string
	http          *http.Client
	modelsOnce    sync.Once
	models        map[string]map[string]any
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

func (r *Renderer) newMount(configDir, projectRoot string, instructions []string) (*Mount, error) {
	if r == nil || r.http == nil {
		return nil, errors.New("opencode renderer requires an HTTP client")
	}
	return &Mount{configDir: configDir, projectRoot: projectRoot, instructions: append([]string(nil), instructions...), http: r.http}, nil
}

func (r *Renderer) RegisterStaticFiles(ctx context.Context, registrar staticfiles.Registrar, configDir, projectRoot string, instructions []string) ([]error, error) {
	mount, err := r.newMount(configDir, projectRoot, instructions)
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
	realConfig, err := m.readSource()
	if err != nil {
		return nil, err
	}
	config := map[string]any{"$schema": "https://opencode.ai/config.json"}
	addSynthetic(config, m.projectRoot, m.instructions, m.syncedModels(ctx, realConfig))
	return marshalConfig(config)
}

func (m *Mount) readSource() (map[string]any, error) {
	result := map[string]any{}
	for _, source := range m.sourceFiles() {
		if _, err := os.Stat(source.path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		data, err := os.ReadFile(source.path)
		if err != nil {
			return nil, err
		}
		if len(bytes.TrimSpace(data)) == 0 {
			continue
		}
		var config map[string]any
		if source.format == formatYAML {
			config, err = decodeYAMLConfig(data)
		} else {
			config, err = decodeConfig(data)
		}
		if err != nil {
			return nil, err
		}
		mergeConfig(result, config)
	}
	return result, nil
}

func (m *Mount) sourceYAMLPath() string {
	return filepath.Join(m.sourceDir(), "opencode.yaml")
}

func (m *Mount) sourceFiles() []sourceFile {
	return []sourceFile{
		{path: filepath.Join(m.sourceDir(), "config.json"), format: formatJSON},
		{path: filepath.Join(m.sourceDir(), "opencode.json"), format: formatJSON},
		{path: filepath.Join(m.sourceDir(), "opencode.jsonc"), format: formatJSON},
		{path: m.sourceYAMLPath(), format: formatYAML},
	}
}

func mergeConfig(dst, src map[string]any) {
	for key, value := range src {
		srcMap, srcOK := value.(map[string]any)
		dstMap, dstOK := dst[key].(map[string]any)
		if srcOK && dstOK {
			mergeConfig(dstMap, srcMap)
			continue
		}
		dst[key] = value
	}
}

func (m *Mount) sourceDir() string {
	return m.configDir
}

func decodeConfig(data []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("parse opencode config: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return nil, fmt.Errorf("parse opencode config: %w", err)
		}
		return nil, errors.New("parse opencode config: multiple JSON values")
	}
	config, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("opencode config must be a JSON object")
	}
	return config, nil
}

func decodeYAMLConfig(data []byte) (map[string]any, error) {
	var value any
	if err := yaml.Unmarshal(data, &value); err != nil {
		return nil, fmt.Errorf("parse opencode config: %w", err)
	}
	if value == nil {
		return map[string]any{}, nil
	}
	normalized, err := normalizeYAML(value)
	if err != nil {
		return nil, err
	}
	config, ok := normalized.(map[string]any)
	if !ok {
		return nil, errors.New("opencode config must be a YAML object")
	}
	return config, nil
}

func normalizeYAML(value any) (any, error) {
	switch v := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(v))
		for key, item := range v {
			normalized, err := normalizeYAML(item)
			if err != nil {
				return nil, err
			}
			result[key] = normalized
		}
		return result, nil
	case map[any]any:
		result := make(map[string]any, len(v))
		for key, item := range v {
			stringKey, ok := key.(string)
			if !ok {
				return nil, fmt.Errorf("opencode config contains non-string YAML key: %v", key)
			}
			normalized, err := normalizeYAML(item)
			if err != nil {
				return nil, err
			}
			result[stringKey] = normalized
		}
		return result, nil
	case []any:
		result := make([]any, len(v))
		for i, item := range v {
			normalized, err := normalizeYAML(item)
			if err != nil {
				return nil, err
			}
			result[i] = normalized
		}
		return result, nil
	default:
		return value, nil
	}
}

func marshalConfig(config map[string]any) ([]byte, error) {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func (m *Mount) syncedModels(ctx context.Context, config map[string]any) map[string]map[string]any {
	m.modelsOnce.Do(func() {
		m.models = m.discoverModels(ctx, config)
	})
	return m.models
}

func (m *Mount) discoverModels(ctx context.Context, config map[string]any) map[string]map[string]any {
	providers, ok := config["provider"].(map[string]any)
	if !ok {
		return nil
	}
	configDir := m.sourceDir()
	models := map[string]map[string]any{}
	for providerID, rawProvider := range providers {
		provider, ok := rawProvider.(map[string]any)
		if !ok || !isOpenAICompatibleProvider(provider) {
			continue
		}
		modelIDs, err := m.fetchProviderModelIDs(ctx, providerID, provider, configDir)
		if err != nil {
			m.modelWarnings = append(m.modelWarnings, fmt.Errorf("fetch OpenCode models for provider %q: %w", providerID, err))
			continue
		}
		models[providerID] = providerModels(modelIDs)
	}
	if len(models) == 0 {
		return nil
	}
	return models
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

func (m *Mount) fetchProviderModelIDs(ctx context.Context, providerID string, provider map[string]any, configDir string) ([]string, error) {
	options := provider["options"].(map[string]any)
	baseURL := strings.TrimSpace(options["baseURL"].(string))
	headers, err := resolveHeaders(providerID, options, configDir)
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

func resolveHeaders(providerID string, options map[string]any, configDir string) (map[string]string, error) {
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
		resolved, err := resolveString(value, configDir)
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
		resolved, err := resolveString(apiKey, configDir)
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

func resolveString(value, configDir string) (string, error) {
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
		if !filepath.IsAbs(path) {
			path = filepath.Join(configDir, path)
		}
		data, err := os.ReadFile(path)
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

func addSyncedModels(config map[string]any, models map[string]map[string]any) {
	if len(models) == 0 {
		return
	}
	providers, ok := config["provider"].(map[string]any)
	if !ok {
		providers = map[string]any{}
		config["provider"] = providers
	}
	for providerID, synced := range models {
		provider, ok := providers[providerID].(map[string]any)
		if !ok {
			provider = map[string]any{}
			providers[providerID] = provider
		}
		provider["models"] = cloneModels(synced)
	}
}

func cloneModels(models map[string]any) map[string]any {
	clone := make(map[string]any, len(models))
	for id, rawModel := range models {
		if model, ok := rawModel.(map[string]any); ok {
			modelClone := make(map[string]any, len(model))
			for key, value := range model {
				modelClone[key] = value
			}
			clone[id] = modelClone
			continue
		}
		clone[id] = rawModel
	}
	return clone
}

func addSynthetic(config map[string]any, projectRoot string, instructions []string, models map[string]map[string]any) {
	mcp := objectAt(config, "mcp")
	mcp["toby"] = syntheticMCP()
	addInstructions(config, instructions)
	addSyncedModels(config, models)
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
		"command": []any{"toby-sandbox", "mcp"},
		"enabled": true,
	}
}

func allowedExternalDirectoryPatterns(projectRoot string) []string {
	return []string{"/tmp", "/tmp/**", projectRoot, filepath.Join(projectRoot, "**")}
}
