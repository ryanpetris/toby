package tobyconfig

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/config/file"
	"petris.dev/toby/internal/context/files"
	"petris.dev/toby/internal/diagnostic/warning"
	"petris.dev/toby/internal/tools/tool"
)

const InstructionsDir = "instructions"

const (
	ProviderTypeAnthropic = "anthropic"
	ProviderTypeOpenAI    = "openai"
)

var substitutionPattern = regexp.MustCompile(`\{(env|file):([^}]+)\}`)

type Service struct {
	Dir    string
	Home   string
	config Config
}

type Config struct {
	Instructions []string
	MCP          map[string]MCPServer
	Permission   PermissionConfig
	Provider     map[string]ProviderConfig
	Sandbox      SandboxConfig
}

type SandboxConfig struct {
	Runtime               RuntimeConfig
	Tools                 tool.ToolStateSettings
	SuppressWarnings      warning.Suppression
	AutoloadProjectConfig *bool
}

type RuntimeConfig struct {
	Default    string
	Docker     DockerSandboxConfig
	Bubblewrap BubblewrapSandboxConfig
}

type DockerSandboxConfig struct {
	Image    string
	Home     string
	Projects string
	Build    tool.DockerBuildConfig
}

type BubblewrapSandboxConfig struct {
	Root string
}

type MCPServer struct {
	raw        map[string]any
	configDirs []string
	home       string
}

type ProviderConfig struct {
	Type      string
	Name      string
	BaseURL   string
	Headers   map[string]string
	Models    map[string]any
	modelsSet bool
}

type PermissionConfig struct {
	Paths map[string]string
}

type sourceFile struct {
	name   string
	format configfile.Format
}

func New(paths config.Paths) (*Service, error) {
	return Load(paths.TobyConfigDir(), paths.Home)
}

func Load(dir, home string) (*Service, error) {
	merged := Config{
		MCP:      map[string]MCPServer{},
		Provider: map[string]ProviderConfig{},
		Permission: PermissionConfig{
			Paths: map[string]string{},
		},
	}
	for _, source := range sourceFiles() {
		path := filepath.Join(dir, source.name)
		if _, err := os.Stat(path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if len(strings.TrimSpace(string(data))) == 0 {
			continue
		}
		raw, err := configfile.Decode(data, source.format, "toby config")
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		parsed, err := parseConfig(raw, filepath.Dir(path), home)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		merged.Merge(parsed)
	}
	if err := merged.Validate(); err != nil {
		return nil, err
	}
	return &Service{Dir: dir, Home: home, config: merged}, nil
}

func sourceFiles() []sourceFile {
	return []sourceFile{
		{name: "config.json", format: configfile.FormatJSON},
		{name: "config.jsonc", format: configfile.FormatJSON},
		{name: "config.yaml", format: configfile.FormatYAML},
		{name: "config.yml", format: configfile.FormatYAML},
	}
}

func parseConfig(raw map[string]any, dir, home string) (Config, error) {
	result := Config{
		MCP:      map[string]MCPServer{},
		Provider: map[string]ProviderConfig{},
		Permission: PermissionConfig{
			Paths: map[string]string{},
		},
	}
	for key, value := range raw {
		switch key {
		case "instructions":
			instructions, err := parseStringList("instructions", value)
			if err != nil {
				return Config{}, err
			}
			result.Instructions = instructions
		case "mcp":
			mcp, err := parseObjectMap("mcp", value)
			if err != nil {
				return Config{}, err
			}
			for name, server := range mcp {
				result.MCP[name] = MCPServer{raw: server}
			}
		case "permission":
			permission, err := parsePermission(value, home)
			if err != nil {
				return Config{}, err
			}
			result.Permission = permission
		case "provider":
			providers, err := parseProviderMap(value)
			if err != nil {
				return Config{}, err
			}
			result.Provider = providers
		case "sandbox":
			sandbox, err := parseSandbox(value, dir, home)
			if err != nil {
				return Config{}, err
			}
			result.Sandbox = sandbox
		default:
			return Config{}, fmt.Errorf("unsupported top-level key %q", key)
		}
	}
	return result, nil
}

func (c *Config) Merge(src Config) {
	c.Instructions = appendDedupeStrings(c.Instructions, src.Instructions)
	if c.MCP == nil {
		c.MCP = map[string]MCPServer{}
	}
	for name, server := range src.MCP {
		if existing, ok := c.MCP[name]; ok {
			merged := existing.Raw()
			configfile.Merge(merged, server.Raw())
			c.MCP[name] = MCPServer{raw: merged}
			continue
		}
		c.MCP[name] = MCPServer{raw: server.Raw()}
	}
	if c.Provider == nil {
		c.Provider = map[string]ProviderConfig{}
	}
	for name, provider := range src.Provider {
		if existing, ok := c.Provider[name]; ok {
			existing.Merge(provider)
			c.Provider[name] = existing
			continue
		}
		c.Provider[name] = provider.Clone()
	}
	if c.Permission.Paths == nil {
		c.Permission.Paths = map[string]string{}
	}
	for pattern, mode := range src.Permission.Paths {
		c.Permission.Paths[pattern] = mode
	}
	c.Sandbox.Merge(src.Sandbox)
}

func (c Config) Validate() error {
	for name, server := range c.MCP {
		typ, _ := server.raw["type"].(string)
		typ = strings.TrimSpace(typ)
		if typ != "" && typ != "local" && typ != "remote" {
			return fmt.Errorf("mcp.%s.type is unsupported", name)
		}
	}
	for name, provider := range c.Provider {
		if provider.Type == "" {
			return fmt.Errorf("provider.%s.type is required", name)
		}
		if !providerTypeSupported(provider.Type) {
			return fmt.Errorf("provider.%s.type is unsupported", name)
		}
		if provider.BaseURL == "" {
			return fmt.Errorf("provider.%s.baseURL is required", name)
		}
	}
	return nil
}

func (c *SandboxConfig) Merge(src SandboxConfig) {
	c.Runtime.Merge(src.Runtime)
	c.Tools.Merge(src.Tools)
	c.SuppressWarnings.Merge(src.SuppressWarnings)
	if src.AutoloadProjectConfig != nil {
		autoload := *src.AutoloadProjectConfig
		c.AutoloadProjectConfig = &autoload
	}
}

func (c SandboxConfig) AutoloadProjectConfigEnabled() bool {
	return c.AutoloadProjectConfig != nil && *c.AutoloadProjectConfig
}

func (c *RuntimeConfig) Merge(src RuntimeConfig) {
	if src.Default != "" {
		c.Default = src.Default
	}
	if src.Docker.Image != "" {
		c.Docker.Image = src.Docker.Image
	}
	if src.Docker.Home != "" {
		c.Docker.Home = src.Docker.Home
	}
	if src.Docker.Projects != "" {
		c.Docker.Projects = src.Docker.Projects
	}
	if src.Docker.Build.IsSet() {
		c.Docker.Build = src.Docker.Build
	}
	if src.Bubblewrap.Root != "" {
		c.Bubblewrap.Root = src.Bubblewrap.Root
	}
}

func (s *Service) Instructions() []string {
	if s == nil {
		return nil
	}
	return append([]string(nil), s.config.Instructions...)
}

func (s *Service) MCPServers() map[string]MCPServer {
	servers := map[string]MCPServer{}
	if s == nil {
		return servers
	}
	configDirs, home := s.resolutionContext()
	for name, server := range s.config.MCP {
		servers[name] = MCPServer{raw: server.Raw(), configDirs: configDirs, home: home}
	}
	return servers
}

func (s *Service) Providers() map[string]ProviderConfig {
	providers := map[string]ProviderConfig{}
	if s == nil {
		return providers
	}
	for name, provider := range s.config.Provider {
		providers[name] = provider.Clone()
	}
	return providers
}

func (s *Service) Provider(name string) (ProviderConfig, bool) {
	if s == nil {
		return ProviderConfig{}, false
	}
	provider, ok := s.config.Provider[name]
	if !ok {
		return ProviderConfig{}, false
	}
	return provider.Clone(), true
}

func (s *Service) resolutionContext() ([]string, string) {
	configDirs := []string{}
	if s != nil && s.Dir != "" {
		configDirs = append(configDirs, s.Dir)
	}
	home := ""
	if s != nil {
		home = s.Home
	}
	if home == "" {
		if detected, err := os.UserHomeDir(); err == nil {
			home = detected
		}
	}
	return configDirs, home
}

func (s *Service) ResolveProviderHeaders(name string, provider ProviderConfig) (http.Header, error) {
	provider = provider.Clone()
	configDirs, home := s.resolutionContext()
	headers := http.Header{}
	for key, value := range provider.Headers {
		resolved, err := resolveString(value, configDirs, home)
		if err != nil {
			return nil, fmt.Errorf("provider %q header %q: %w", name, key, err)
		}
		headers.Set(key, resolved)
	}
	return headers, nil
}

func (s *Service) Permission() PermissionConfig {
	permission := PermissionConfig{Paths: map[string]string{}}
	if s == nil {
		return permission
	}
	for pattern, mode := range s.config.Permission.Paths {
		permission.Paths[pattern] = mode
	}
	return permission
}

func (s *Service) Sandbox() SandboxConfig {
	if s == nil {
		return SandboxConfig{}
	}
	return s.config.Sandbox
}

func (s *Service) RegisterContextFiles(ctx context.Context, service *contextfiles.Service) error {
	if s == nil {
		return nil
	}
	hostPaths, err := s.instructionHostPaths()
	if err != nil {
		return err
	}
	seenNames := map[string]bool{}
	for _, hostPath := range hostPaths {
		data, err := os.ReadFile(hostPath)
		if err != nil {
			return fmt.Errorf("read instruction file %s: %w", hostPath, err)
		}
		name, err := uniqueInstructionName(filepath.Base(hostPath), seenNames)
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(filepath.Join(InstructionsDir, name))
		if _, err := service.AddInstruction(ctx, rel, data, 0o400); err != nil {
			return err
		}
	}
	return nil
}

func (s MCPServer) Raw() map[string]any {
	return configfile.CloneMap(s.raw)
}

func (s MCPServer) Enabled() bool {
	if value, ok := s.raw["enabled"].(bool); ok {
		return value
	}
	return true
}

func (s MCPServer) HTTPProxyable() bool {
	return MCPServerHTTPProxyable(s.raw)
}

func (s MCPServer) URL() string {
	return MCPServerURL(s.raw)
}

func (s MCPServer) Headers() (http.Header, error) {
	headers := http.Header{}
	if err := mergeHeaderMap(headers, s.raw["headers"]); err != nil {
		return nil, fmt.Errorf("headers: %w", err)
	}
	for key, values := range headers {
		for i, value := range values {
			resolved, err := resolveString(value, s.configDirs, s.home)
			if err != nil {
				return nil, fmt.Errorf("headers %q: %w", key, err)
			}
			values[i] = resolved
		}
	}
	return headers, nil
}

func MCPServerHTTPProxyable(server map[string]any) bool {
	typ, _ := server["type"].(string)
	typ = strings.TrimSpace(typ)
	switch typ {
	case "remote":
		return true
	case "":
		if _, ok := server["command"]; ok {
			return false
		}
		url, _ := server["url"].(string)
		return strings.TrimSpace(url) != ""
	default:
		return false
	}
}

func MCPServerURL(server map[string]any) string {
	url, _ := server["url"].(string)
	return strings.TrimSpace(url)
}

func mergeHeaderMap(headers http.Header, raw any) error {
	if raw == nil {
		return nil
	}
	values, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("must be an object")
	}
	for name, rawValue := range values {
		switch value := rawValue.(type) {
		case string:
			headers.Set(name, value)
		case []any:
			headers.Del(name)
			for _, item := range value {
				text, ok := item.(string)
				if !ok {
					return fmt.Errorf("header %q entries must be strings", name)
				}
				headers.Add(name, text)
			}
		default:
			return fmt.Errorf("header %q value must be a string or string array", name)
		}
	}
	return nil
}

func (p ProviderConfig) Raw() map[string]any {
	raw := map[string]any{}
	if p.Type != "" {
		raw["type"] = p.Type
	}
	if p.Name != "" {
		raw["name"] = p.Name
	}
	if p.BaseURL != "" {
		raw["baseURL"] = p.BaseURL
	}
	if len(p.Headers) > 0 {
		headers := map[string]any{}
		for key, value := range p.Headers {
			headers[key] = value
		}
		raw["headers"] = headers
	}
	if p.modelsSet {
		raw["models"] = configfile.CloneMap(p.Models)
	}
	return raw
}

func (p ProviderConfig) Clone() ProviderConfig {
	clone := p
	if p.Headers != nil {
		clone.Headers = make(map[string]string, len(p.Headers))
		for key, value := range p.Headers {
			clone.Headers[key] = value
		}
	}
	if p.Models != nil {
		clone.Models = configfile.CloneMap(p.Models)
	}
	return clone
}

func (p *ProviderConfig) Merge(src ProviderConfig) {
	if src.Type != "" {
		p.Type = src.Type
	}
	if src.Name != "" {
		p.Name = src.Name
	}
	if src.BaseURL != "" {
		p.BaseURL = src.BaseURL
	}
	if len(src.Headers) > 0 {
		if p.Headers == nil {
			p.Headers = map[string]string{}
		}
		for key, value := range src.Headers {
			p.Headers[key] = value
		}
	}
	if src.modelsSet {
		if p.Models == nil {
			p.Models = map[string]any{}
		}
		configfile.Merge(p.Models, src.Models)
		p.modelsSet = true
	}
}

func (p ProviderConfig) HasModels() bool {
	return p.modelsSet
}

func parseStringList(label string, raw any) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an array", label)
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		value, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("%s entries must be strings", label)
		}
		result = append(result, value)
	}
	return result, nil
}

func parseObjectMap(label string, raw any) (map[string]map[string]any, error) {
	if raw == nil {
		return nil, nil
	}
	items, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an object", label)
	}
	result := make(map[string]map[string]any, len(items))
	for name, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s.%s must be an object", label, name)
		}
		result[name] = configfile.CloneMap(item)
	}
	return result, nil
}

func parseProviderMap(raw any) (map[string]ProviderConfig, error) {
	if raw == nil {
		return nil, nil
	}
	items, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("provider must be an object")
	}
	providers := make(map[string]ProviderConfig, len(items))
	for name, rawProvider := range items {
		provider, ok := rawProvider.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("provider.%s must be an object", name)
		}
		parsed, err := parseProviderConfig(name, provider)
		if err != nil {
			return nil, err
		}
		providers[name] = parsed
	}
	return providers, nil
}

func parseProviderConfig(name string, raw map[string]any) (ProviderConfig, error) {
	var cfg ProviderConfig
	for key, value := range raw {
		switch key {
		case "type":
			typ, ok := value.(string)
			if !ok {
				return ProviderConfig{}, fmt.Errorf("provider.%s.type must be a string", name)
			}
			cfg.Type = strings.TrimSpace(typ)
		case "name":
			text, ok := value.(string)
			if !ok {
				return ProviderConfig{}, fmt.Errorf("provider.%s.name must be a string", name)
			}
			cfg.Name = text
		case "baseURL":
			text, ok := value.(string)
			if !ok {
				return ProviderConfig{}, fmt.Errorf("provider.%s.baseURL must be a string", name)
			}
			cfg.BaseURL = strings.TrimSpace(text)
		case "headers":
			headers, err := parseProviderHeaders(name, value)
			if err != nil {
				return ProviderConfig{}, err
			}
			cfg.Headers = headers
		case "models":
			models, ok := value.(map[string]any)
			if !ok {
				return ProviderConfig{}, fmt.Errorf("provider.%s.models must be an object", name)
			}
			cfg.Models = configfile.CloneMap(models)
			cfg.modelsSet = true
		default:
			return ProviderConfig{}, fmt.Errorf("unsupported provider.%s key %q", name, key)
		}
	}
	if cfg.Type != "" && !providerTypeSupported(cfg.Type) {
		return ProviderConfig{}, fmt.Errorf("provider.%s.type is unsupported", name)
	}
	return cfg, nil
}

func providerTypeSupported(typ string) bool {
	switch typ {
	case ProviderTypeAnthropic, ProviderTypeOpenAI:
		return true
	default:
		return false
	}
}

func parseProviderHeaders(name string, raw any) (map[string]string, error) {
	if raw == nil {
		return nil, nil
	}
	items, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("provider.%s.headers must be an object", name)
	}
	headers := make(map[string]string, len(items))
	for key, rawValue := range items {
		value, ok := rawValue.(string)
		if !ok {
			return nil, fmt.Errorf("provider.%s.headers.%s must be a string", name, key)
		}
		headers[key] = value
	}
	return headers, nil
}

func resolveString(value string, configDirs []string, home string) (string, error) {
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
		path := config.ExpandHome(target, home)
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

func parsePermission(raw any, home string) (PermissionConfig, error) {
	permission := PermissionConfig{Paths: map[string]string{}}
	if raw == nil {
		return permission, nil
	}
	items, ok := raw.(map[string]any)
	if !ok {
		return PermissionConfig{}, fmt.Errorf("permission must be an object")
	}
	for key, value := range items {
		if key != "paths" {
			return PermissionConfig{}, fmt.Errorf("unsupported permission key %q", key)
		}
		paths, ok := value.(map[string]any)
		if !ok {
			return PermissionConfig{}, fmt.Errorf("permission.paths must be an object")
		}
		for pattern, rawMode := range paths {
			mode, ok := rawMode.(string)
			if !ok {
				return PermissionConfig{}, fmt.Errorf("permission.paths[%q] must be a string", pattern)
			}
			permission.Paths[config.ExpandHome(pattern, home)] = mode
		}
	}
	return permission, nil
}

func parseSandbox(raw any, dir, home string) (SandboxConfig, error) {
	if raw == nil {
		return SandboxConfig{}, nil
	}
	items, ok := raw.(map[string]any)
	if !ok {
		return SandboxConfig{}, fmt.Errorf("sandbox must be an object")
	}
	var cfg SandboxConfig
	for key, value := range items {
		switch key {
		case "runtime":
			runtime, err := parseRuntime(value, dir, home)
			if err != nil {
				return SandboxConfig{}, err
			}
			cfg.Runtime = runtime
		case "tools":
			tools, err := parseSandboxTools(value, dir, home)
			if err != nil {
				return SandboxConfig{}, err
			}
			cfg.Tools = tools
		case "suppressWarnings":
			suppression, err := warning.ParseSuppression(value, "sandbox.suppressWarnings")
			if err != nil {
				return SandboxConfig{}, err
			}
			cfg.SuppressWarnings = suppression
		case "autoloadProjectConfig":
			autoload, ok := value.(bool)
			if !ok {
				return SandboxConfig{}, fmt.Errorf("sandbox.autoloadProjectConfig must be a boolean")
			}
			cfg.AutoloadProjectConfig = &autoload
		default:
			return SandboxConfig{}, fmt.Errorf("unsupported sandbox key %q", key)
		}
	}
	return cfg, nil
}

func parseRuntime(raw any, dir, home string) (RuntimeConfig, error) {
	switch value := raw.(type) {
	case string:
		return RuntimeConfig{Default: strings.TrimSpace(value)}, nil
	case map[string]any:
		var cfg RuntimeConfig
		for key, item := range value {
			switch key {
			case "default":
				name, ok := item.(string)
				if !ok {
					return RuntimeConfig{}, fmt.Errorf("sandbox.runtime.default must be a string")
				}
				cfg.Default = strings.TrimSpace(name)
			case "docker":
				docker, err := parseDockerSandbox(item, dir, home)
				if err != nil {
					return RuntimeConfig{}, err
				}
				cfg.Docker = docker
			case "bubblewrap":
				bubblewrap, err := parseBubblewrapSandbox(item, dir, home)
				if err != nil {
					return RuntimeConfig{}, err
				}
				cfg.Bubblewrap = bubblewrap
			default:
				return RuntimeConfig{}, fmt.Errorf("unsupported sandbox.runtime key %q", key)
			}
		}
		return cfg, nil
	default:
		return RuntimeConfig{}, fmt.Errorf("sandbox.runtime must be a string or object")
	}
}

func parseDockerSandbox(raw any, dir, home string) (DockerSandboxConfig, error) {
	items, ok := raw.(map[string]any)
	if !ok {
		return DockerSandboxConfig{}, fmt.Errorf("sandbox.runtime.docker must be an object")
	}
	var cfg DockerSandboxConfig
	for key, value := range items {
		switch key {
		case "image":
			item, ok := value.(string)
			if !ok {
				return DockerSandboxConfig{}, fmt.Errorf("sandbox.runtime.docker.image must be a string")
			}
			cfg.Image = strings.TrimSpace(item)
		case "home":
			item, ok := value.(string)
			if !ok {
				return DockerSandboxConfig{}, fmt.Errorf("sandbox.runtime.docker.home must be a string")
			}
			cfg.Home = strings.TrimSpace(item)
		case "projects":
			item, ok := value.(string)
			if !ok {
				return DockerSandboxConfig{}, fmt.Errorf("sandbox.runtime.docker.projects must be a string")
			}
			cfg.Projects = strings.TrimSpace(item)
		case "build":
			build, err := parseDockerBuild(value, dir, home)
			if err != nil {
				return DockerSandboxConfig{}, err
			}
			cfg.Build = build
		default:
			return DockerSandboxConfig{}, fmt.Errorf("unsupported sandbox.runtime.docker key %q", key)
		}
	}
	return cfg, nil
}

func parseDockerBuild(raw any, dir, home string) (tool.DockerBuildConfig, error) {
	items, ok := raw.(map[string]any)
	if !ok {
		return tool.DockerBuildConfig{}, fmt.Errorf("sandbox.runtime.docker.build must be an object")
	}
	contextValue := "."
	dockerfileValue := "Dockerfile"
	for key, value := range items {
		item, ok := value.(string)
		if !ok {
			return tool.DockerBuildConfig{}, fmt.Errorf("sandbox.runtime.docker.build.%s must be a string", key)
		}
		item = strings.TrimSpace(item)
		if item == "" {
			return tool.DockerBuildConfig{}, fmt.Errorf("sandbox.runtime.docker.build.%s must not be empty", key)
		}
		switch key {
		case "context":
			contextValue = item
		case "dockerfile":
			dockerfileValue = item
		default:
			return tool.DockerBuildConfig{}, fmt.Errorf("unsupported sandbox.runtime.docker.build key %q", key)
		}
	}
	contextDir, err := resolveConfigPath(contextValue, dir, home)
	if err != nil {
		return tool.DockerBuildConfig{}, fmt.Errorf("sandbox.runtime.docker.build.context: %w", err)
	}
	dockerfile := config.ExpandHome(dockerfileValue, home)
	if !filepath.IsAbs(dockerfile) {
		dockerfile = filepath.Join(contextDir, dockerfile)
	}
	return tool.DockerBuildConfig{Context: contextDir, Dockerfile: dockerfile}, nil
}

func parseBubblewrapSandbox(raw any, dir, home string) (BubblewrapSandboxConfig, error) {
	items, ok := raw.(map[string]any)
	if !ok {
		return BubblewrapSandboxConfig{}, fmt.Errorf("sandbox.runtime.bubblewrap must be an object")
	}
	var cfg BubblewrapSandboxConfig
	for key, value := range items {
		item, ok := value.(string)
		if !ok {
			return BubblewrapSandboxConfig{}, fmt.Errorf("sandbox.runtime.bubblewrap.%s must be a string", key)
		}
		item = strings.TrimSpace(item)
		switch key {
		case "root":
			root, err := resolveConfigPath(item, dir, home)
			if err != nil {
				return BubblewrapSandboxConfig{}, fmt.Errorf("sandbox.runtime.bubblewrap.root: %w", err)
			}
			cfg.Root = root
		default:
			return BubblewrapSandboxConfig{}, fmt.Errorf("unsupported sandbox.runtime.bubblewrap key %q", key)
		}
	}
	return cfg, nil
}

func resolveConfigPath(value, dir, home string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("must not be empty")
	}
	value = config.ExpandHome(value, home)
	if filepath.IsAbs(value) {
		return value, nil
	}
	return filepath.Join(dir, value), nil
}

func parseSandboxTools(raw any, dir, home string) (tool.ToolStateSettings, error) {
	items, ok := raw.(map[string]any)
	if !ok {
		return tool.ToolStateSettings{}, fmt.Errorf("sandbox.tools must be an object")
	}
	settings := tool.ToolStateSettings{}
	for name, rawTool := range items {
		name = strings.TrimSpace(name)
		if name == "" {
			return tool.ToolStateSettings{}, fmt.Errorf("sandbox.tools keys must not be empty")
		}
		toolConfig, ok := rawTool.(map[string]any)
		if !ok {
			return tool.ToolStateSettings{}, fmt.Errorf("sandbox.tools.%s must be an object", name)
		}
		cfg, err := parseSandboxTool(name, toolConfig, dir, home)
		if err != nil {
			return tool.ToolStateSettings{}, err
		}
		if cfg.State == "" && cfg.StateRoot == "" {
			continue
		}
		if name == "default" {
			settings.Default = cfg
			continue
		}
		if settings.Tools == nil {
			settings.Tools = map[string]tool.ToolStateConfig{}
		}
		settings.Tools[name] = cfg
	}
	return settings, nil
}

func parseSandboxTool(name string, raw map[string]any, dir, home string) (tool.ToolStateConfig, error) {
	var cfg tool.ToolStateConfig
	for key, value := range raw {
		switch key {
		case "state":
			rawState, ok := value.(string)
			if !ok {
				return tool.ToolStateConfig{}, fmt.Errorf("sandbox.tools.%s.state must be a string", name)
			}
			parsed, err := tool.ParseToolState(rawState)
			if err != nil {
				return tool.ToolStateConfig{}, fmt.Errorf("sandbox.tools.%s.state: %w", name, err)
			}
			cfg.State = parsed
		case "stateRoot":
			rawRoot, ok := value.(string)
			if !ok {
				return tool.ToolStateConfig{}, fmt.Errorf("sandbox.tools.%s.stateRoot must be a string", name)
			}
			root, err := tool.ResolveStateRoot(rawRoot, home, dir)
			if err != nil {
				return tool.ToolStateConfig{}, fmt.Errorf("sandbox.tools.%s.stateRoot: %w", name, err)
			}
			cfg.StateRoot = root
		default:
			return tool.ToolStateConfig{}, fmt.Errorf("unsupported sandbox.tools.%s key %q", name, key)
		}
	}
	return cfg, nil
}

func appendDedupeStrings(dst, src []string) []string {
	result := make([]string, 0, len(dst)+len(src))
	seen := map[string]bool{}
	for _, item := range append(append([]string{}, dst...), src...) {
		if seen[item] {
			continue
		}
		seen[item] = true
		result = append(result, item)
	}
	return result
}

func (s *Service) instructionHostPaths() ([]string, error) {
	paths := make([]string, 0, len(s.config.Instructions))
	seen := map[string]bool{}
	for _, pattern := range s.config.Instructions {
		matches, err := s.resolveInstructionPattern(pattern)
		if err != nil {
			return nil, err
		}
		for _, match := range matches {
			if seen[match] {
				continue
			}
			seen[match] = true
			paths = append(paths, match)
		}
	}
	return paths, nil
}

func (s *Service) resolveInstructionPattern(pattern string) ([]string, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil, nil
	}
	path := config.ExpandHome(pattern, s.Home)
	if !filepath.IsAbs(path) {
		path = filepath.Join(s.Dir, path)
	}
	if hasGlobMeta(path) {
		matches, err := filepath.Glob(path)
		if err != nil {
			return nil, fmt.Errorf("invalid instruction pattern %q: %w", pattern, err)
		}
		sort.Strings(matches)
		return cleanInstructionPaths(matches)
	}
	return cleanInstructionPaths([]string{path})
}

func cleanInstructionPaths(paths []string) ([]string, error) {
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		result = append(result, abs)
	}
	return result, nil
}

func hasGlobMeta(path string) bool {
	return strings.ContainsAny(path, "*?[")
}

func uniqueInstructionName(name string, seen map[string]bool) (string, error) {
	if name == "" || name == "." || name == string(filepath.Separator) {
		return "", fmt.Errorf("invalid instruction filename %q", name)
	}
	if !seen[name] {
		seen[name] = true
		return name, nil
	}
	for {
		suffix, err := randomSuffix()
		if err != nil {
			return "", err
		}
		candidate := insertBeforeExtension(name, suffix)
		if !seen[candidate] {
			seen[candidate] = true
			return candidate, nil
		}
	}
}

func randomSuffix() (string, error) {
	var bytes [3]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}

func insertBeforeExtension(name, suffix string) string {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	if base == "" {
		return name + "." + suffix
	}
	return base + "." + suffix + ext
}
