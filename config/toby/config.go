// Package tobyconfig loads and resolves Toby's host configuration: it deep-merges
// the config source files, strict-decodes them into the typed schema, and exposes
// the resolved instructions, container image/build, MCP servers, providers,
// permissions, settings, and tool config.
package tobyconfig

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"petris.dev/toby/config"
	containerconfig "petris.dev/toby/config/container"
	configfile "petris.dev/toby/config/file"
	"petris.dev/toby/container/layout"
	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/diagnostic/warning"
	"petris.dev/toby/tools"
)

const InstructionsDir = "instructions"

const (
	ProviderTypeAnthropic = "anthropic"
	ProviderTypeOpenAI    = "openai"
)

const (
	MCPTypeLocal  = "local"
	MCPTypeRemote = "remote"

	MCPTransportStdio = "stdio"
	MCPTransportHTTP  = "http"
)

var substitutionPattern = regexp.MustCompile(`\{(env|file):([^}]+)\}`)

type Service struct {
	Dir    string
	Home   string
	config Config
}

// Config is the resolved Toby host configuration.
type Config struct {
	Instructions []string
	MCP          MCPConfig
	Permission   PermissionConfig
	Provider     map[string]ProviderConfig
	Settings     SettingsConfig
	Tools        map[string]ToolConfig
	Container    ContainerConfig
}

// MCPConfig groups the MCP sidecar default image with the configured servers.
type MCPConfig struct {
	Image   string
	Servers map[string]MCPServer
}

// ContainerConfig is the resolved `container:` block: the image to run and an
// optional build that produces it.
type ContainerConfig struct {
	Image string
	Build tools.Build
}

type SettingsConfig struct {
	MountProfile          string
	SuppressWarnings      warning.Suppression
	AutoloadProjectConfig *bool
	Debug                 *bool
	Yolo                  *bool
}

type ToolConfig struct {
	MountProfile string
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

// fileSchema is the strict decode target for a merged config. Open passthrough
// fields (MCP server bodies, provider models) keep their map[string]any type so
// strict decoding never recurses into them.
type fileSchema struct {
	Instruction []string                   `json:"instruction" yaml:"instruction"`
	Container   containerconfig.Config     `json:"container" yaml:"container"`
	MCP         mcpSchema                  `json:"mcp" yaml:"mcp"`
	Permission  permissionSchema           `json:"permission" yaml:"permission"`
	Provider    map[string]*providerSchema `json:"provider" yaml:"provider"`
	Settings    settingsSchema             `json:"settings" yaml:"settings"`
	Tool        map[string]*toolSchema     `json:"tool" yaml:"tool"`
}

type mcpSchema struct {
	Image  string                    `json:"image" yaml:"image"`
	Server map[string]map[string]any `json:"server" yaml:"server"`
}

type permissionSchema struct {
	Paths map[string]string `json:"paths" yaml:"paths"`
}

type providerSchema struct {
	Type    string            `json:"type" yaml:"type"`
	Name    string            `json:"name" yaml:"name"`
	BaseURL string            `json:"baseURL" yaml:"baseURL"`
	Headers map[string]string `json:"headers" yaml:"headers"`
	Models  *map[string]any   `json:"models" yaml:"models"`
}

type settingsSchema struct {
	MountProfile          string   `json:"mountProfile" yaml:"mountProfile"`
	SuppressWarnings      []string `json:"suppressWarnings" yaml:"suppressWarnings"`
	AutoloadProjectConfig *bool    `json:"autoloadProjectConfig" yaml:"autoloadProjectConfig"`
	Debug                 *bool    `json:"debug" yaml:"debug"`
	Yolo                  *bool    `json:"yolo" yaml:"yolo"`
}

type toolSchema struct {
	MountProfile string `json:"mountProfile" yaml:"mountProfile"`
}

func New(paths config.Paths) (*Service, error) {
	return Load(paths.TobyConfigDir(), paths.Home)
}

// Load reads the config source files from dir, deep-merges them as generic maps,
// strict-decodes the result, and resolves it into a Service.
func Load(dir, home string) (*Service, error) {
	merged := map[string]any{}
	for _, name := range sourceFiles() {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		fileMap := map[string]any{}
		if err := configfile.DecodeFile(path, &fileMap); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		configfile.Merge(merged, fileMap)
	}
	var schema fileSchema
	if err := configfile.DecodeInto(merged, &schema); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(dir, "config"), err)
	}
	cfg, err := resolve(schema, dir, home)
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Service{Dir: dir, Home: home, config: cfg}, nil
}

func sourceFiles() []string {
	return []string{"config.json", "config.yaml", "config.yml"}
}

func emptyConfig() Config {
	return Config{
		MCP:        MCPConfig{Servers: map[string]MCPServer{}},
		Provider:   map[string]ProviderConfig{},
		Permission: PermissionConfig{Paths: map[string]string{}},
		Tools:      map[string]ToolConfig{},
	}
}

// resolve turns the decoded schema into a resolved Config: it expands paths and
// substitution patterns, parses warning suppression, and clones open passthrough
// bodies so the Config never shares mutable structure with the decoded maps.
func resolve(schema fileSchema, dir, home string) (Config, error) {
	result := emptyConfig()
	result.Instructions = append([]string(nil), schema.Instruction...)

	result.MCP.Image = strings.TrimSpace(schema.MCP.Image)
	for name, body := range schema.MCP.Server {
		if body == nil {
			return Config{}, fmt.Errorf("mcp.server.%s must be an object", name)
		}
		clean := configfile.CloneMap(body)
		configfile.NormalizeNumbers(clean)
		result.MCP.Servers[name] = MCPServer{raw: clean}
	}

	for pattern, mode := range schema.Permission.Paths {
		result.Permission.Paths[config.ExpandHome(pattern, home)] = mode
	}

	for name, provider := range schema.Provider {
		if provider == nil {
			return Config{}, fmt.Errorf("provider.%s must be an object", name)
		}
		resolved, err := provider.resolve(name)
		if err != nil {
			return Config{}, err
		}
		result.Provider[name] = resolved
	}

	settings, err := schema.Settings.resolve()
	if err != nil {
		return Config{}, err
	}
	result.Settings = settings

	for rawName, tool := range schema.Tool {
		name := strings.TrimSpace(rawName)
		if name == "" {
			return Config{}, fmt.Errorf("tool keys must not be empty")
		}
		cfg := ToolConfig{}
		if tool != nil {
			cfg.MountProfile = strings.TrimSpace(tool.MountProfile)
		}
		result.Tools[name] = cfg
	}

	build, err := containerconfig.ResolveBuild(schema.Container.Build, dir, home)
	if err != nil {
		return Config{}, err
	}
	result.Container = ContainerConfig{Image: strings.TrimSpace(schema.Container.Image), Build: build}

	return result, nil
}

func (p *providerSchema) resolve(name string) (ProviderConfig, error) {
	cfg := ProviderConfig{
		Type:    strings.TrimSpace(p.Type),
		Name:    p.Name,
		BaseURL: strings.TrimSpace(p.BaseURL),
	}
	if len(p.Headers) > 0 {
		cfg.Headers = make(map[string]string, len(p.Headers))
		for key, value := range p.Headers {
			cfg.Headers[key] = value
		}
	}
	if p.Models != nil {
		cfg.Models = configfile.CloneMap(*p.Models)
		configfile.NormalizeNumbers(cfg.Models)
		cfg.modelsSet = true
	}
	if cfg.Type != "" && !providerTypeSupported(cfg.Type) {
		return ProviderConfig{}, fmt.Errorf("provider.%s.type is unsupported", name)
	}
	return cfg, nil
}

func (s settingsSchema) resolve() (SettingsConfig, error) {
	cfg := SettingsConfig{MountProfile: strings.TrimSpace(s.MountProfile)}
	if s.SuppressWarnings != nil {
		suppression, err := warning.SuppressionFromList(s.SuppressWarnings, "settings.suppressWarnings")
		if err != nil {
			return SettingsConfig{}, err
		}
		cfg.SuppressWarnings = suppression
	}
	if s.AutoloadProjectConfig != nil {
		autoload := *s.AutoloadProjectConfig
		cfg.AutoloadProjectConfig = &autoload
	}
	if s.Debug != nil {
		debug := *s.Debug
		cfg.Debug = &debug
	}
	if s.Yolo != nil {
		yolo := *s.Yolo
		cfg.Yolo = &yolo
	}
	return cfg, nil
}

func (c Config) Validate() error {
	for name, server := range c.MCP.Servers {
		typ := server.Type()
		if typ != "" && typ != MCPTypeLocal && typ != MCPTypeRemote {
			return fmt.Errorf("mcp.server.%s.type is unsupported", name)
		}
		if server.Local() {
			transport := server.Transport()
			if transport != MCPTransportStdio && transport != MCPTransportHTTP {
				return fmt.Errorf("mcp.server.%s.transport is unsupported", name)
			}
			if _, err := server.CommandParts(); err != nil {
				return err
			}
			if transport == MCPTransportHTTP && server.Port() <= 0 {
				return fmt.Errorf("mcp.server.%s.port is required for http transport", name)
			}
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

func (c SettingsConfig) AutoloadProjectConfigEnabled() bool {
	return c.AutoloadProjectConfig != nil && *c.AutoloadProjectConfig
}

func (c SettingsConfig) DebugEnabled() bool {
	return c.Debug != nil && *c.Debug
}

func (c SettingsConfig) YoloEnabled() bool {
	return c.Yolo != nil && *c.Yolo
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
	for name, server := range s.config.MCP.Servers {
		servers[name] = MCPServer{raw: server.Raw(), configDirs: configDirs, home: home}
	}
	return servers
}

// MCPImage returns the default MCP sidecar image (`mcp.image`).
func (s *Service) MCPImage() string {
	if s == nil {
		return ""
	}
	return s.config.MCP.Image
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

// defaultPermissionMode is the mode applied to the paths Toby injects by default.
const defaultPermissionMode = "allow"

// defaultPermissionPaths returns the permission paths Toby injects into supported
// tool configs by default. They grant access to the sandbox projects root, /tmp,
// and the common home cache/state directories used by development tooling such as
// Go, npm, and pip. Each directory is paired with a recursive glob so tools that
// match on patterns cover the subtree. The projects root itself is added, not the
// individual mounted project paths.
func defaultPermissionPaths() map[string]string {
	dirs := []string{"/tmp", layout.Workspace, filepath.Join(layout.Home, "go"), filepath.Join(layout.Home, ".cache")}
	result := map[string]string{}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		result[dir] = defaultPermissionMode
		result[dir+"/**"] = defaultPermissionMode
	}
	return result
}

// PermissionPaths returns the permission paths for supported tool configs: Toby's
// default injected paths merged with the user-configured permission.paths. User
// entries override defaults for the same path.
func (s *Service) PermissionPaths() map[string]string {
	result := defaultPermissionPaths()
	if s == nil {
		return result
	}
	for pattern, mode := range s.config.Permission.Paths {
		result[pattern] = mode
	}
	return result
}

func (s *Service) ToolMountProfiles() map[string]string {
	profiles := map[string]string{}
	if s == nil {
		return profiles
	}
	for name, cfg := range s.config.Tools {
		if strings.TrimSpace(cfg.MountProfile) != "" {
			profiles[name] = strings.TrimSpace(cfg.MountProfile)
		}
	}
	return profiles
}

func (s *Service) Settings() SettingsConfig {
	if s == nil {
		return SettingsConfig{}
	}
	return s.config.Settings
}

// Container returns the resolved `container:` block (image + build).
func (s *Service) Container() ContainerConfig {
	if s == nil {
		return ContainerConfig{}
	}
	return s.config.Container
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
		if _, err := service.AddInstruction(ctx, rel, data, 0o644); err != nil {
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

func (s MCPServer) Type() string {
	typ, _ := s.raw["type"].(string)
	typ = strings.TrimSpace(typ)
	if typ != "" {
		return typ
	}
	if strings.TrimSpace(s.URL()) != "" {
		return MCPTypeRemote
	}
	if _, ok := s.raw["command"]; ok {
		return MCPTypeLocal
	}
	return ""
}

func (s MCPServer) Local() bool {
	return s.Type() == MCPTypeLocal
}

func (s MCPServer) Remote() bool {
	return s.Type() == MCPTypeRemote
}

func (s MCPServer) Transport() string {
	transport, _ := s.raw["transport"].(string)
	transport = strings.TrimSpace(transport)
	if transport == "" && s.Local() {
		return MCPTransportStdio
	}
	return transport
}

// Image returns this server's sidecar image override (`mcp.server.<n>.image`),
// or the empty string when it should fall back to the configured defaults.
func (s MCPServer) Image() string {
	image, _ := s.raw["image"].(string)
	return strings.TrimSpace(image)
}

func (s MCPServer) CommandParts() ([]string, error) {
	return mcpCommandParts("mcp server", s.raw["command"])
}

func (s MCPServer) Port() int {
	return intFromAny(s.raw["port"])
}

func (s MCPServer) Path() string {
	path, _ := s.raw["path"].(string)
	path = strings.TrimSpace(path)
	if path == "" {
		return "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return path
}

func (s MCPServer) Environment() (map[string]string, error) {
	raw, ok := s.raw["env"]
	if !ok {
		raw, ok = s.raw["environment"]
	}
	if !ok || raw == nil {
		return nil, nil
	}
	values, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("env must be an object")
	}
	env := make(map[string]string, len(values))
	for name, rawValue := range values {
		value, ok := rawValue.(string)
		if !ok {
			return nil, fmt.Errorf("env.%s must be a string", name)
		}
		resolved, err := resolveString(value, s.configDirs, s.home)
		if err != nil {
			return nil, fmt.Errorf("env.%s: %w", name, err)
		}
		env[name] = resolved
	}
	return env, nil
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
	case MCPTypeRemote, MCPTypeLocal:
		return true
	case "":
		if _, ok := server["command"]; ok {
			return true
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

func mcpCommandParts(label string, raw any) ([]string, error) {
	switch command := raw.(type) {
	case string:
		command = strings.TrimSpace(command)
		if command == "" {
			return nil, fmt.Errorf("%s command is empty", label)
		}
		return []string{command}, nil
	case []any:
		if len(command) == 0 {
			return nil, fmt.Errorf("%s command is empty", label)
		}
		parts := make([]string, 0, len(command))
		for i, item := range command {
			part, ok := item.(string)
			if !ok || part == "" {
				if i == 0 {
					return nil, fmt.Errorf("%s command must start with a string", label)
				}
				return nil, fmt.Errorf("%s command arguments must be strings", label)
			}
			parts = append(parts, part)
		}
		return parts, nil
	default:
		return nil, fmt.Errorf("%s command is required", label)
	}
}

func intFromAny(raw any) int {
	switch value := raw.(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		parsed, err := strconv.Atoi(value.String())
		if err == nil {
			return parsed
		}
		floatValue, err := strconv.ParseFloat(value.String(), 64)
		if err == nil {
			return int(floatValue)
		}
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err == nil {
			return parsed
		}
	}
	return 0
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

func (p ProviderConfig) HasModels() bool {
	return p.modelsSet
}

func providerTypeSupported(typ string) bool {
	switch typ {
	case ProviderTypeAnthropic, ProviderTypeOpenAI:
		return true
	default:
		return false
	}
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
