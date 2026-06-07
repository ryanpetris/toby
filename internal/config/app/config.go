// Package appconfig loads and resolves Toby's host configuration: it deep-merges
// the config source files, strict-decodes them into the typed schema, and exposes
// the resolved instructions, container image/build, MCP servers, providers,
// permissions, settings, and tool config.
package appconfig

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
	configfile "petris.dev/toby/config/file"
	"petris.dev/toby/container/layout"
	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/diagnostic/warning"
	containerconfig "petris.dev/toby/internal/config/container"
	"petris.dev/toby/internal/permission"
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
	// toolMountProfileOverrides are per-tool mount profiles folded in from a
	// launch (CLI/launch-config), layered over the config-derived profiles.
	toolMountProfileOverrides map[string]string
}

// Config is the resolved Toby host configuration.
type Config struct {
	Instructions []string
	MCP          MCPConfig
	Permissions  PermissionConfig
	Providers    map[string]ProviderConfig
	Settings     SettingsConfig
	Tools        map[string]ToolConfig
	Container    ContainerConfig
}

// MCPConfig holds the configured MCP servers plus the default sidecar image and
// build that apply to any server without its own image.
type MCPConfig struct {
	Image   string
	Build   tools.Build
	Servers map[string]MCPServer
}

// ContainerConfig is the resolved `container:` block: the image to run, an
// optional build that produces it, and the host ports to publish. Ports are
// launch-only — they come from the project config or the --publish flag and are
// folded in via WithOverrides; the host config schema does not carry them.
type ContainerConfig struct {
	Image string
	Build tools.Build
	Ports []string
}

type SettingsConfig struct {
	MountProfile          string
	SuppressWarnings      warning.Suppression
	AutoloadProjectConfig *bool
	Debug                 *bool
	Yolo                  *bool
	ManagedTerminal       *bool
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
	URL       string
	Headers   map[string]string
	Models    map[string]any
	modelsSet bool
}

type PermissionConfig struct {
	Paths map[string]string
	// Actions maps an action id (its RPC method name, e.g. "git.commit") to the
	// configured rule that governs whether it is allowed, denied, or asked.
	Actions map[string]permission.Rule
}

// fileSchema is the strict decode target for a merged config. Open passthrough
// fields (MCP server bodies, provider models) keep their map[string]any type so
// strict decoding never recurses into them.
type fileSchema struct {
	Instructions []string               `json:"instructions" yaml:"instructions"`
	Container    containerconfig.Config `json:"container" yaml:"container"`
	MCP          mcpSchema              `json:"mcps" yaml:"mcps"`
	Permissions  permissionSchema       `json:"permissions" yaml:"permissions"`
	Providers    providerBlockSchema    `json:"providers" yaml:"providers"`
	Settings     settingsSchema         `json:"settings" yaml:"settings"`
	Tools        map[string]*toolSchema `json:"tools" yaml:"tools"`
}

// providerBlockSchema is the `providers` block: the map of provider definitions
// under `servers`, mirroring the `mcps` block. (Image/build defaults for stdio
// providers can follow here later.)
type providerBlockSchema struct {
	Servers map[string]*providerSchema `json:"servers" yaml:"servers"`
}

// mcpSchema is the `mcps` block: the default sidecar image/build (the shared
// container block, so a server without its own image can fall back to it) plus
// the map of configured MCP servers.
type mcpSchema struct {
	containerconfig.Config `yaml:",inline"`
	Servers                map[string]map[string]any `json:"servers" yaml:"servers"`
}

type permissionSchema struct {
	Paths   map[string]string `json:"paths" yaml:"paths"`
	Actions map[string]string `json:"actions" yaml:"actions"`
}

type providerSchema struct {
	Type    string            `json:"type" yaml:"type"`
	Name    string            `json:"name" yaml:"name"`
	URL     string            `json:"url" yaml:"url"`
	Headers map[string]string `json:"headers" yaml:"headers"`
	Models  *map[string]any   `json:"models" yaml:"models"`
}

type settingsSchema struct {
	MountProfile          string   `json:"mountProfile" yaml:"mountProfile"`
	SuppressWarnings      []string `json:"suppressWarnings" yaml:"suppressWarnings"`
	AutoloadProjectConfig *bool    `json:"autoloadProjectConfig" yaml:"autoloadProjectConfig"`
	Debug                 *bool    `json:"debug" yaml:"debug"`
	Yolo                  *bool    `json:"yolo" yaml:"yolo"`
	ManagedTerminal       *bool    `json:"managedTerminal" yaml:"managedTerminal"`
}

type toolSchema struct {
	MountProfile string `json:"mountProfile" yaml:"mountProfile"`
}

func New(paths config.Paths) (*Service, error) {
	return Load(paths.TobyConfigDir(), paths.Home)
}

// Load reads the config source files from dir, deep-merges them as generic maps,
// strict-decodes the result, and resolves it into a Service. The generic merge
// has a later file's value replace an earlier one's; the list-valued instructions
// and settings.suppressWarnings keys are instead unioned across files (first
// occurrence wins on order), so each file contributes additively.
func Load(dir, home string) (*Service, error) {
	merged := map[string]any{}
	var instructions, suppressWarnings []any
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
		if list, ok := fileMap["instructions"].([]any); ok {
			instructions = append(instructions, list...)
		}
		if settings, ok := fileMap["settings"].(map[string]any); ok {
			if list, ok := settings["suppressWarnings"].([]any); ok {
				suppressWarnings = append(suppressWarnings, list...)
			}
		}
		configfile.Merge(merged, fileMap)
	}
	if len(instructions) > 0 {
		merged["instructions"] = dedupeList(instructions)
	}
	if len(suppressWarnings) > 0 {
		settings, ok := merged["settings"].(map[string]any)
		if !ok {
			settings = map[string]any{}
			merged["settings"] = settings
		}
		settings["suppressWarnings"] = dedupeList(suppressWarnings)
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

// dedupeList returns items with duplicate string entries removed, preserving the
// order of first occurrence. Non-string entries are kept as-is for the strict
// decoder to reject.
func dedupeList(items []any) []any {
	seen := make(map[string]bool, len(items))
	out := make([]any, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			if seen[s] {
				continue
			}
			seen[s] = true
		}
		out = append(out, item)
	}
	return out
}

func emptyConfig() Config {
	return Config{
		MCP:         MCPConfig{Servers: map[string]MCPServer{}},
		Providers:   map[string]ProviderConfig{},
		Permissions: PermissionConfig{Paths: map[string]string{}, Actions: map[string]permission.Rule{}},
		Tools:       map[string]ToolConfig{},
	}
}

// resolve turns the decoded schema into a resolved Config: it expands paths and
// substitution patterns, parses warning suppression, and clones open passthrough
// bodies so the Config never shares mutable structure with the decoded maps.
func resolve(schema fileSchema, dir, home string) (Config, error) {
	result := emptyConfig()
	result.Instructions = append([]string(nil), schema.Instructions...)

	mcpBuild, err := containerconfig.ResolveBuild(schema.MCP.Build, dir, home)
	if err != nil {
		return Config{}, err
	}
	result.MCP.Image = strings.TrimSpace(schema.MCP.Image)
	result.MCP.Build = mcpBuild
	for name, body := range schema.MCP.Servers {
		if body == nil {
			return Config{}, fmt.Errorf("mcps.servers.%s must be an object", name)
		}
		clean := configfile.CloneMap(body)
		configfile.NormalizeNumbers(clean)
		result.MCP.Servers[name] = MCPServer{raw: clean}
	}

	for pattern, mode := range schema.Permissions.Paths {
		result.Permissions.Paths[config.ExpandHome(pattern, home)] = mode
	}

	for action, value := range schema.Permissions.Actions {
		rule, err := permission.ParseRule(value)
		if err != nil {
			return Config{}, fmt.Errorf("permissions.actions.%s: %w", action, err)
		}
		result.Permissions.Actions[action] = rule
	}

	for name, provider := range schema.Providers.Servers {
		if provider == nil {
			return Config{}, fmt.Errorf("providers.servers.%s must be an object", name)
		}
		resolved, err := provider.resolve(name)
		if err != nil {
			return Config{}, err
		}
		result.Providers[name] = resolved
	}

	settings, err := schema.Settings.resolve()
	if err != nil {
		return Config{}, err
	}
	result.Settings = settings

	for rawName, tool := range schema.Tools {
		name := strings.TrimSpace(rawName)
		if name == "" {
			return Config{}, fmt.Errorf("tools keys must not be empty")
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
		Type: strings.TrimSpace(p.Type),
		Name: p.Name,
		URL:  strings.TrimSpace(p.URL),
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
	if s.ManagedTerminal != nil {
		managed := *s.ManagedTerminal
		cfg.ManagedTerminal = &managed
	}
	return cfg, nil
}

func (c Config) Validate() error {
	for name, server := range c.MCP.Servers {
		typ := server.Type()
		if typ != "" && typ != MCPTypeLocal && typ != MCPTypeRemote {
			return fmt.Errorf("mcps.servers.%s.type is unsupported", name)
		}
		if server.Local() {
			transport := server.Transport()
			if transport != MCPTransportStdio && transport != MCPTransportHTTP {
				return fmt.Errorf("mcps.servers.%s.transport is unsupported", name)
			}
			if _, err := server.CommandParts(); err != nil {
				return err
			}
			if transport == MCPTransportHTTP && server.Port() <= 0 {
				return fmt.Errorf("mcps.servers.%s.port is required for http transport", name)
			}
		}
	}
	for name, provider := range c.Providers {
		if provider.Type == "" {
			return fmt.Errorf("providers.servers.%s.type is required", name)
		}
		if !providerTypeSupported(provider.Type) {
			return fmt.Errorf("providers.servers.%s.type is unsupported", name)
		}
		if provider.URL == "" {
			return fmt.Errorf("providers.servers.%s.url is required", name)
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

// ManagedTerminalEnabled reports whether Toby interposes its managed terminal for the
// interactive foreground tool (raw-passthrough shadow plus the approval modal). It
// defaults to on; only an explicit `settings.managedTerminal: false` (or
// --managed-terminal=false) turns it off, falling back to a plain passthrough — which
// means approval prompts cannot be shown, so anything not explicitly allowed is denied.
func (c SettingsConfig) ManagedTerminalEnabled() bool {
	return c.ManagedTerminal == nil || *c.ManagedTerminal
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

func (s *Service) Providers() map[string]ProviderConfig {
	providers := map[string]ProviderConfig{}
	if s == nil {
		return providers
	}
	for name, provider := range s.config.Providers {
		providers[name] = provider.Clone()
	}
	return providers
}

func (s *Service) Provider(name string) (ProviderConfig, bool) {
	if s == nil {
		return ProviderConfig{}, false
	}
	provider, ok := s.config.Providers[name]
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
	out := PermissionConfig{Paths: map[string]string{}, Actions: map[string]permission.Rule{}}
	if s == nil {
		return out
	}
	for pattern, mode := range s.config.Permissions.Paths {
		out.Paths[pattern] = mode
	}
	for action, rule := range s.config.Permissions.Actions {
		out.Actions[action] = rule
	}
	return out
}

// defaultPermissionMode is the mode applied to the paths Toby injects by default.
const defaultPermissionMode = "allow"

// defaultPermissionPaths returns the permission paths Toby injects into supported
// tool configs by default. They grant access to the sandbox projects root, /tmp,
// and the common home cache/state directories used by development tooling such as
// Go, npm, and pip. Permission paths are always directories, so each is passed to
// the consuming tool config verbatim; tools that match on globs (opencode) add a
// recursive form themselves. The projects root itself is added, not the individual
// mounted project paths. When yolo is enabled the filesystem root is granted so the
// tool may reach any path.
func defaultPermissionPaths(yolo bool) map[string]string {
	dirs := []string{"/tmp", layout.Workspace, filepath.Join(layout.Home, "go"), filepath.Join(layout.Home, ".cache")}
	result := map[string]string{}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		result[dir] = defaultPermissionMode
	}
	if yolo {
		result["/"] = defaultPermissionMode
	}
	return result
}

// PermissionPaths returns the permission paths for supported tool configs: Toby's
// default injected paths merged with the user-configured permission.paths. User
// entries override defaults for the same path. When yolo is enabled (in the
// effective config, which already folds in the --yolo flag) the filesystem root
// is granted.
func (s *Service) PermissionPaths() map[string]string {
	result := defaultPermissionPaths(s.YoloEnabled())
	if s == nil {
		return result
	}
	for pattern, mode := range s.config.Permissions.Paths {
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
	for name, profile := range s.toolMountProfileOverrides {
		profiles[name] = profile
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

// mcpConfig returns the resolved `mcps:` block (default sidecar image/build +
// servers).
func (s *Service) mcpConfig() MCPConfig {
	if s == nil {
		return MCPConfig{}
	}
	return s.config.MCP
}

// Service-level reads of config-corresponding launch values. These are the single
// source of truth: a launch's CLI flags and launch-config overrides are folded in
// via WithOverrides before the per-launch graph reads them.
func (s *Service) YoloEnabled() bool            { return s.Settings().YoloEnabled() }
func (s *Service) DebugEnabled() bool           { return s.Settings().DebugEnabled() }
func (s *Service) ManagedTerminalEnabled() bool { return s.Settings().ManagedTerminalEnabled() }

// PermissionRule returns the configured rule for an action (by its method-name id,
// e.g. "git.commit"), or RuleUnset when nothing is configured.
func (s *Service) PermissionRule(action string) permission.Rule {
	if s == nil {
		return permission.RuleUnset
	}
	return s.config.Permissions.Actions[action]
}
func (s *Service) Image() string         { return s.Container().Image }
func (s *Service) Build() tools.Build    { return s.Container().Build }
func (s *Service) Ports() []string       { return s.Container().Ports }
func (s *Service) MCPImage() string      { return s.mcpConfig().Image }
func (s *Service) MCPBuild() tools.Build { return s.mcpConfig().Build }
func (s *Service) MountProfile() string  { return s.Settings().MountProfile }

// LaunchOverrides carries the config-corresponding values a single launch may
// override (from CLI flags and the launch-config file). Folding these into the
// config via WithOverrides keeps the Service the single source of truth.
type LaunchOverrides struct {
	MountProfile      string
	Image             string
	Build             tools.Build
	Ports             []string
	Debug             *bool
	Yolo              *bool
	ManagedTerminal   *bool
	ToolMountProfiles map[string]string
	SuppressWarnings  warning.Suppression
}

// WithOverrides returns a new Service whose config has the launch overrides folded
// in (override wins; ToolMountProfiles and SuppressWarnings merge over the config
// base). The receiver is not mutated, so the process-wide singleton is unaffected.
func (s *Service) WithOverrides(o LaunchOverrides) *Service {
	if s == nil {
		return nil
	}
	next := *s

	settings := s.config.Settings
	settings.SuppressWarnings = s.config.Settings.SuppressWarnings.Clone()
	settings.SuppressWarnings.Merge(o.SuppressWarnings)
	if o.MountProfile != "" {
		settings.MountProfile = o.MountProfile
	}
	if o.Debug != nil {
		debug := *o.Debug
		settings.Debug = &debug
	}
	if o.Yolo != nil {
		yolo := *o.Yolo
		settings.Yolo = &yolo
	}
	if o.ManagedTerminal != nil {
		managed := *o.ManagedTerminal
		settings.ManagedTerminal = &managed
	}
	next.config.Settings = settings

	container := s.config.Container
	if o.Image != "" {
		container.Image = o.Image
	}
	if o.Build.IsSet() {
		container.Build = o.Build
	}
	if len(o.Ports) > 0 {
		container.Ports = append(append([]string(nil), container.Ports...), o.Ports...)
	}
	next.config.Container = container

	if len(o.ToolMountProfiles) > 0 {
		merged := make(map[string]string, len(s.toolMountProfileOverrides)+len(o.ToolMountProfiles))
		for name, profile := range s.toolMountProfileOverrides {
			merged[name] = profile
		}
		for name, profile := range o.ToolMountProfiles {
			merged[name] = profile
		}
		next.toolMountProfileOverrides = merged
	}

	return &next
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

// Image returns this server's sidecar image override (`mcps.servers.<n>.image`),
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
	if p.URL != "" {
		raw["url"] = p.URL
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
