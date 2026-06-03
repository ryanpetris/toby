package tobyconfig

import (
	"bytes"
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

	"gopkg.in/yaml.v3"
	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/config/file"
	"petris.dev/toby/internal/context/files"
	"petris.dev/toby/internal/diagnostic/warning"
	sandboxmount "petris.dev/toby/internal/sandbox/mount"
	sandboxpath "petris.dev/toby/internal/sandbox/path"
	"petris.dev/toby/internal/tools/helpers"
	"petris.dev/toby/internal/tools/tool"
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

	MCPRuntimeDocker     = "docker"
	MCPRuntimeBubblewrap = "bubblewrap"
)

var substitutionPattern = regexp.MustCompile(`\{(env|file):([^}]+)\}`)

type Service struct {
	Dir    string
	Home   string
	config Config
}

type Config struct {
	Instructions  []string
	MCP           map[string]MCPServer
	Permission    PermissionConfig
	Provider      map[string]ProviderConfig
	MountProfiles sandboxmount.Profiles
	Settings      SettingsConfig
	Tools         map[string]ToolConfig
	Sandbox       SandboxConfig
}

type SandboxConfig struct {
	Runtime RuntimeConfig
	MCP     MCPSandboxConfig
}

type MCPSandboxConfig struct {
	Runtime MCPRuntimeConfig
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

type RuntimeConfig struct {
	Default    string
	Docker     DockerSandboxConfig
	Bubblewrap BubblewrapSandboxConfig
}

type MCPRuntimeConfig struct {
	Type   string
	Docker MCPDockerRuntimeConfig
}

type MCPDockerRuntimeConfig struct {
	Image string
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

type rawConfig struct {
	Instructions  []string                      `yaml:"instructions" json:"instructions"`
	MCP           map[string]map[string]any     `yaml:"mcps" json:"mcps"`
	Permission    rawPermissionConfig           `yaml:"permissions" json:"permissions"`
	Provider      map[string]*rawProviderConfig `yaml:"providers" json:"providers"`
	MountProfiles map[string]*rawMountProfile   `yaml:"mountProfiles" json:"mountProfiles"`
	Settings      rawSettingsConfig             `yaml:"settings" json:"settings"`
	Tools         map[string]*rawToolConfig     `yaml:"tools" json:"tools"`
	Sandbox       rawSandboxConfig              `yaml:"sandbox" json:"sandbox"`
}

type rawPermissionConfig struct {
	Paths map[string]string `yaml:"paths" json:"paths"`
}

type rawProviderConfig struct {
	Type    string            `yaml:"type" json:"type"`
	Name    string            `yaml:"name" json:"name"`
	BaseURL string            `yaml:"baseURL" json:"baseURL"`
	Headers map[string]string `yaml:"headers" json:"headers"`
	Models  *map[string]any   `yaml:"models" json:"models"`
}

type rawMountProfile struct {
	Backing  string `yaml:"backing" json:"backing"`
	HostRoot string `yaml:"hostRoot" json:"hostRoot"`
}

type rawSettingsConfig struct {
	MountProfile          string         `yaml:"mountProfile" json:"mountProfile"`
	SuppressWarnings      rawSuppression `yaml:"suppressWarnings" json:"suppressWarnings"`
	AutoloadProjectConfig *bool          `yaml:"autoloadProjectConfig" json:"autoloadProjectConfig"`
	Debug                 *bool          `yaml:"debug" json:"debug"`
	Yolo                  *bool          `yaml:"yolo" json:"yolo"`
}

type rawToolConfig struct {
	MountProfile string `yaml:"mountProfile" json:"mountProfile"`
}

type rawSandboxConfig struct {
	Runtime rawRuntimeConfig    `yaml:"runtime" json:"runtime"`
	MCP     rawMCPSandboxConfig `yaml:"mcp" json:"mcp"`
}

type rawSandboxFields struct {
	Runtime rawRuntimeConfig    `yaml:"runtime" json:"runtime"`
	MCP     rawMCPSandboxConfig `yaml:"mcp" json:"mcp"`
}

type rawRuntimeConfig struct {
	Default    string
	Docker     rawDockerSandboxConfig
	Bubblewrap rawBubblewrapSandboxConfig
}

type rawRuntimeFields struct {
	Default    string                     `yaml:"default" json:"default"`
	Docker     rawDockerSandboxConfig     `yaml:"docker" json:"docker"`
	Bubblewrap rawBubblewrapSandboxConfig `yaml:"bubblewrap" json:"bubblewrap"`
}

type rawDockerSandboxConfig struct {
	Image    string                `yaml:"image" json:"image"`
	Home     string                `yaml:"home" json:"home"`
	Projects string                `yaml:"projects" json:"projects"`
	Build    *rawDockerBuildConfig `yaml:"build" json:"build"`
}

type rawDockerBuildConfig struct {
	Context    rawString `yaml:"context" json:"context"`
	Dockerfile rawString `yaml:"dockerfile" json:"dockerfile"`
}

type rawBubblewrapSandboxConfig struct {
	Root string `yaml:"root" json:"root"`
}

type rawMCPSandboxConfig struct {
	Runtime rawMCPRuntimeConfig `yaml:"runtime" json:"runtime"`
}

type rawMCPSandboxFields struct {
	Runtime rawMCPRuntimeConfig `yaml:"runtime" json:"runtime"`
}

type rawMCPRuntimeConfig struct {
	Type   string
	Docker rawMCPDockerRuntimeConfig
}

type rawMCPRuntimeFields struct {
	Type   string                    `yaml:"type" json:"type"`
	Docker rawMCPDockerRuntimeConfig `yaml:"docker" json:"docker"`
}

type rawMCPDockerRuntimeConfig struct {
	Image string `yaml:"image" json:"image"`
}

type rawSuppression struct {
	Set   bool
	Value any
}

type rawString struct {
	Set   bool
	Value string
}

func New(paths config.Paths) (*Service, error) {
	return Load(paths.TobyConfigDir(), paths.Home)
}

func Load(dir, home string) (*Service, error) {
	merged := emptyConfig()
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
		var raw rawConfig
		if err := configfile.DecodeInto(data, source.format, "toby config", &raw); err != nil {
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

func emptyConfig() Config {
	return Config{
		MCP:      map[string]MCPServer{},
		Provider: map[string]ProviderConfig{},
		Permission: PermissionConfig{
			Paths: map[string]string{},
		},
		MountProfiles: sandboxmount.Profiles{},
		Tools:         map[string]ToolConfig{},
	}
}

func parseConfig(raw rawConfig, dir, home string) (Config, error) {
	result := emptyConfig()
	result.Instructions = append([]string(nil), raw.Instructions...)

	for name, server := range raw.MCP {
		if server == nil {
			return Config{}, fmt.Errorf("mcps.%s must be an object", name)
		}
		result.MCP[name] = MCPServer{raw: configfile.CloneMap(server)}
	}

	result.Permission = raw.Permission.toPermission(home)

	providers, err := rawProviderMap(raw.Provider)
	if err != nil {
		return Config{}, err
	}
	result.Provider = providers

	profiles, err := rawMountProfiles(raw.MountProfiles, dir, home)
	if err != nil {
		return Config{}, err
	}
	result.MountProfiles = profiles

	settings, err := raw.Settings.toSettings()
	if err != nil {
		return Config{}, err
	}
	result.Settings = settings

	tools, err := rawToolMap(raw.Tools)
	if err != nil {
		return Config{}, err
	}
	result.Tools = tools

	sandbox, err := raw.Sandbox.toSandbox(dir, home)
	if err != nil {
		return Config{}, err
	}
	result.Sandbox = sandbox

	return result, nil
}

func (r rawPermissionConfig) toPermission(home string) PermissionConfig {
	permission := PermissionConfig{Paths: map[string]string{}}
	for pattern, mode := range r.Paths {
		permission.Paths[config.ExpandHome(pattern, home)] = mode
	}
	return permission
}

func rawProviderMap(raw map[string]*rawProviderConfig) (map[string]ProviderConfig, error) {
	providers := make(map[string]ProviderConfig, len(raw))
	for name, rawProvider := range raw {
		if rawProvider == nil {
			return nil, fmt.Errorf("providers.%s must be an object", name)
		}
		provider, err := rawProvider.toProvider(name)
		if err != nil {
			return nil, err
		}
		providers[name] = provider
	}
	return providers, nil
}

func (r rawProviderConfig) toProvider(name string) (ProviderConfig, error) {
	cfg := ProviderConfig{
		Type:    strings.TrimSpace(r.Type),
		Name:    r.Name,
		BaseURL: strings.TrimSpace(r.BaseURL),
	}
	if len(r.Headers) > 0 {
		cfg.Headers = make(map[string]string, len(r.Headers))
		for key, value := range r.Headers {
			cfg.Headers[key] = value
		}
	}
	if r.Models != nil {
		cfg.Models = configfile.CloneMap(*r.Models)
		cfg.modelsSet = true
	}
	if cfg.Type != "" && !providerTypeSupported(cfg.Type) {
		return ProviderConfig{}, fmt.Errorf("providers.%s.type is unsupported", name)
	}
	return cfg, nil
}

func rawMountProfiles(raw map[string]*rawMountProfile, dir, home string) (sandboxmount.Profiles, error) {
	profiles := sandboxmount.Profiles{}
	for rawName, rawProfile := range raw {
		name := strings.TrimSpace(rawName)
		if name == "" {
			return nil, fmt.Errorf("mountProfiles keys must not be empty")
		}
		if rawProfile == nil {
			return nil, fmt.Errorf("mountProfiles.%s must be an object", name)
		}
		profile, err := rawProfile.toMountProfile("mountProfiles."+name, dir, home)
		if err != nil {
			return nil, err
		}
		profiles[name] = profile
	}
	return profiles, nil
}

func (r rawMountProfile) toMountProfile(label string, dir, home string) (sandboxmount.BackingConfig, error) {
	var cfg sandboxmount.BackingConfig
	if r.Backing != "" {
		backing, err := helpers.ParseMountBacking(r.Backing)
		if err != nil {
			return sandboxmount.BackingConfig{}, fmt.Errorf("%s.backing: %w", label, err)
		}
		cfg.Backing = backing
	}
	if r.HostRoot != "" {
		root, err := helpers.ResolveMountHostRoot(r.HostRoot, home, dir)
		if err != nil {
			return sandboxmount.BackingConfig{}, fmt.Errorf("%s.hostRoot: %w", label, err)
		}
		cfg.HostRoot = root
	}
	return cfg, nil
}

func (r rawSettingsConfig) toSettings() (SettingsConfig, error) {
	cfg := SettingsConfig{MountProfile: strings.TrimSpace(r.MountProfile)}
	if r.SuppressWarnings.Set {
		suppression, err := warning.ParseSuppression(r.SuppressWarnings.Value, "settings.suppressWarnings")
		if err != nil {
			return SettingsConfig{}, err
		}
		cfg.SuppressWarnings = suppression
	}
	if r.AutoloadProjectConfig != nil {
		autoload := *r.AutoloadProjectConfig
		cfg.AutoloadProjectConfig = &autoload
	}
	if r.Debug != nil {
		debug := *r.Debug
		cfg.Debug = &debug
	}
	if r.Yolo != nil {
		yolo := *r.Yolo
		cfg.Yolo = &yolo
	}
	return cfg, nil
}

func rawToolMap(raw map[string]*rawToolConfig) (map[string]ToolConfig, error) {
	tools := map[string]ToolConfig{}
	for rawName, rawTool := range raw {
		name := strings.TrimSpace(rawName)
		if name == "" {
			return nil, fmt.Errorf("tools keys must not be empty")
		}
		if rawTool == nil {
			tools[name] = ToolConfig{}
			continue
		}
		tools[name] = ToolConfig{MountProfile: strings.TrimSpace(rawTool.MountProfile)}
	}
	return tools, nil
}

func (r rawSandboxConfig) toSandbox(dir, home string) (SandboxConfig, error) {
	runtime, err := r.Runtime.toRuntime(dir, home)
	if err != nil {
		return SandboxConfig{}, err
	}
	mcp, err := r.MCP.toMCPSandbox()
	if err != nil {
		return SandboxConfig{}, err
	}
	return SandboxConfig{Runtime: runtime, MCP: mcp}, nil
}

func (r rawRuntimeConfig) toRuntime(dir, home string) (RuntimeConfig, error) {
	docker, err := r.Docker.toDocker(dir, home)
	if err != nil {
		return RuntimeConfig{}, err
	}
	bubblewrap, err := r.Bubblewrap.toBubblewrap(dir, home)
	if err != nil {
		return RuntimeConfig{}, err
	}
	return RuntimeConfig{Default: strings.TrimSpace(r.Default), Docker: docker, Bubblewrap: bubblewrap}, nil
}

func (r rawDockerSandboxConfig) toDocker(dir, home string) (DockerSandboxConfig, error) {
	cfg := DockerSandboxConfig{Image: strings.TrimSpace(r.Image), Home: strings.TrimSpace(r.Home), Projects: strings.TrimSpace(r.Projects)}
	if r.Build != nil {
		build, err := r.Build.toDockerBuild(dir, home)
		if err != nil {
			return DockerSandboxConfig{}, err
		}
		cfg.Build = build
	}
	return cfg, nil
}

func (r rawDockerBuildConfig) toDockerBuild(dir, home string) (tool.DockerBuildConfig, error) {
	contextValue := "."
	if r.Context.Set {
		contextValue = strings.TrimSpace(r.Context.Value)
		if contextValue == "" {
			return tool.DockerBuildConfig{}, fmt.Errorf("sandbox.runtime.docker.build.context must not be empty")
		}
	}
	dockerfileValue := "Dockerfile"
	if r.Dockerfile.Set {
		dockerfileValue = strings.TrimSpace(r.Dockerfile.Value)
		if dockerfileValue == "" {
			return tool.DockerBuildConfig{}, fmt.Errorf("sandbox.runtime.docker.build.dockerfile must not be empty")
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

func (r rawBubblewrapSandboxConfig) toBubblewrap(dir, home string) (BubblewrapSandboxConfig, error) {
	if strings.TrimSpace(r.Root) == "" {
		return BubblewrapSandboxConfig{}, nil
	}
	root, err := resolveConfigPath(r.Root, dir, home)
	if err != nil {
		return BubblewrapSandboxConfig{}, fmt.Errorf("sandbox.runtime.bubblewrap.root: %w", err)
	}
	return BubblewrapSandboxConfig{Root: root}, nil
}

func (r rawMCPSandboxConfig) toMCPSandbox() (MCPSandboxConfig, error) {
	runtime, err := r.Runtime.toMCPRuntime()
	if err != nil {
		return MCPSandboxConfig{}, err
	}
	return MCPSandboxConfig{Runtime: runtime}, nil
}

func (r rawMCPRuntimeConfig) toMCPRuntime() (MCPRuntimeConfig, error) {
	typ := strings.TrimSpace(r.Type)
	return MCPRuntimeConfig{Type: typ, Docker: MCPDockerRuntimeConfig{Image: strings.TrimSpace(r.Docker.Image)}}, nil
}

func (r *rawRuntimeConfig) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		if value.Tag != "!!str" {
			return fmt.Errorf("sandbox.runtime must be a string or object")
		}
		r.Default = strings.TrimSpace(value.Value)
		return nil
	case yaml.MappingNode:
		var fields rawRuntimeFields
		if err := decodeYAMLNodeStrict(value, &fields); err != nil {
			return err
		}
		r.Default = fields.Default
		r.Docker = fields.Docker
		r.Bubblewrap = fields.Bubblewrap
		return nil
	default:
		return fmt.Errorf("sandbox.runtime must be a string or object")
	}
}

func (r *rawSandboxConfig) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode && value.Tag == "!!null" {
		return nil
	}
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("sandbox must be an object")
	}
	for i := 0; i+1 < len(value.Content); i += 2 {
		key := value.Content[i].Value
		if key != "runtime" && key != "mcp" {
			return fmt.Errorf("unsupported sandbox key %q", key)
		}
	}
	var fields rawSandboxFields
	if err := decodeYAMLNodeStrict(value, &fields); err != nil {
		return err
	}
	r.Runtime = fields.Runtime
	r.MCP = fields.MCP
	return nil
}

func (r *rawSandboxConfig) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	var items map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &items); err != nil {
		return fmt.Errorf("sandbox must be an object")
	}
	for key := range items {
		if key != "runtime" && key != "mcp" {
			return fmt.Errorf("unsupported sandbox key %q", key)
		}
	}
	var fields rawSandboxFields
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(&fields); err != nil {
		return err
	}
	r.Runtime = fields.Runtime
	r.MCP = fields.MCP
	return nil
}

func (r *rawRuntimeConfig) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return fmt.Errorf("sandbox.runtime must be a string or object")
	}
	if trimmed[0] == '"' {
		var value string
		if err := json.Unmarshal(trimmed, &value); err != nil {
			return err
		}
		r.Default = strings.TrimSpace(value)
		return nil
	}
	var fields rawRuntimeFields
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(&fields); err != nil {
		return err
	}
	r.Default = fields.Default
	r.Docker = fields.Docker
	r.Bubblewrap = fields.Bubblewrap
	return nil
}

func (r *rawMCPSandboxConfig) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode && value.Tag == "!!null" {
		return nil
	}
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("sandbox.mcp must be an object")
	}
	for i := 0; i+1 < len(value.Content); i += 2 {
		key := value.Content[i].Value
		if key != "runtime" {
			return fmt.Errorf("unsupported sandbox.mcp key %q", key)
		}
	}
	var fields rawMCPSandboxFields
	if err := decodeYAMLNodeStrict(value, &fields); err != nil {
		return err
	}
	r.Runtime = fields.Runtime
	return nil
}

func (r *rawMCPSandboxConfig) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	var items map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &items); err != nil {
		return fmt.Errorf("sandbox.mcp must be an object")
	}
	for key := range items {
		if key != "runtime" {
			return fmt.Errorf("unsupported sandbox.mcp key %q", key)
		}
	}
	var fields rawMCPSandboxFields
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(&fields); err != nil {
		return err
	}
	r.Runtime = fields.Runtime
	return nil
}

func (r *rawMCPRuntimeConfig) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		if value.Tag != "!!str" {
			return fmt.Errorf("sandbox.mcp.runtime must be a string or object")
		}
		r.Type = strings.TrimSpace(value.Value)
		return nil
	case yaml.MappingNode:
		var fields rawMCPRuntimeFields
		if err := decodeYAMLNodeStrict(value, &fields); err != nil {
			return err
		}
		r.Type = fields.Type
		r.Docker = fields.Docker
		return nil
	case 0:
		return nil
	default:
		return fmt.Errorf("sandbox.mcp.runtime must be a string or object")
	}
}

func (r *rawMCPRuntimeConfig) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	if trimmed[0] == '"' {
		var value string
		if err := json.Unmarshal(trimmed, &value); err != nil {
			return err
		}
		r.Type = strings.TrimSpace(value)
		return nil
	}
	var fields rawMCPRuntimeFields
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(&fields); err != nil {
		return err
	}
	r.Type = fields.Type
	r.Docker = fields.Docker
	return nil
}

func (s *rawSuppression) UnmarshalYAML(value *yaml.Node) error {
	s.Set = true
	var decoded any
	if err := value.Decode(&decoded); err != nil {
		return err
	}
	s.Value = decoded
	return nil
}

func (s *rawSuppression) UnmarshalJSON(data []byte) error {
	s.Set = true
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	s.Value = decoded
	return nil
}

func (s *rawString) UnmarshalYAML(value *yaml.Node) error {
	s.Set = true
	if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
		return fmt.Errorf("must be a string")
	}
	s.Value = value.Value
	return nil
}

func (s *rawString) UnmarshalJSON(data []byte) error {
	s.Set = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return fmt.Errorf("must be a string")
	}
	return json.Unmarshal(data, &s.Value)
}

func decodeYAMLNodeStrict(value *yaml.Node, dest any) error {
	data, err := yaml.Marshal(value)
	if err != nil {
		return err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	return decoder.Decode(dest)
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
	c.MountProfiles.Merge(src.MountProfiles)
	c.Settings.Merge(src.Settings)
	if c.Tools == nil {
		c.Tools = map[string]ToolConfig{}
	}
	for name, tool := range src.Tools {
		existing := c.Tools[name]
		existing.Merge(tool)
		c.Tools[name] = existing
	}
}

func (c Config) Validate() error {
	for name, server := range c.MCP {
		typ := server.Type()
		if typ != "" && typ != MCPTypeLocal && typ != MCPTypeRemote {
			return fmt.Errorf("mcps.%s.type is unsupported", name)
		}
		if server.Local() {
			transport := server.Transport()
			if transport != MCPTransportStdio && transport != MCPTransportHTTP {
				return fmt.Errorf("mcps.%s.transport is unsupported", name)
			}
			if _, err := server.CommandParts(); err != nil {
				return err
			}
			if transport == MCPTransportHTTP && server.Port() <= 0 {
				return fmt.Errorf("mcps.%s.port is required for http transport", name)
			}
		}
	}
	for name, provider := range c.Provider {
		if provider.Type == "" {
			return fmt.Errorf("providers.%s.type is required", name)
		}
		if !providerTypeSupported(provider.Type) {
			return fmt.Errorf("providers.%s.type is unsupported", name)
		}
		if provider.BaseURL == "" {
			return fmt.Errorf("providers.%s.baseURL is required", name)
		}
	}
	return nil
}

func (c *SandboxConfig) Merge(src SandboxConfig) {
	c.Runtime.Merge(src.Runtime)
	c.MCP.Merge(src.MCP)
}

func (c *MCPSandboxConfig) Merge(src MCPSandboxConfig) {
	c.Runtime.Merge(src.Runtime)
}

func (c *SettingsConfig) Merge(src SettingsConfig) {
	if src.MountProfile != "" {
		c.MountProfile = src.MountProfile
	}
	c.SuppressWarnings.Merge(src.SuppressWarnings)
	if src.AutoloadProjectConfig != nil {
		autoload := *src.AutoloadProjectConfig
		c.AutoloadProjectConfig = &autoload
	}
	if src.Debug != nil {
		debug := *src.Debug
		c.Debug = &debug
	}
	if src.Yolo != nil {
		yolo := *src.Yolo
		c.Yolo = &yolo
	}
}

func (c *ToolConfig) Merge(src ToolConfig) {
	if src.MountProfile != "" {
		c.MountProfile = src.MountProfile
	}
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

func (c *MCPRuntimeConfig) Merge(src MCPRuntimeConfig) {
	if src.Type != "" {
		c.Type = src.Type
	}
	if src.Docker.Image != "" {
		c.Docker.Image = src.Docker.Image
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

// defaultPermissionMode is the mode applied to the paths Toby injects by default.
const defaultPermissionMode = "allow"

// defaultPermissionPaths returns the permission paths Toby injects into supported
// tool configs by default. They grant access to the sandbox projects root, /tmp,
// and the common home cache/state directories used by development tooling such as
// Go, npm, and pip. Each directory is paired with a recursive glob so tools that
// match on patterns cover the subtree. The projects root itself is added, not the
// individual mounted project paths.
func defaultPermissionPaths(paths sandboxpath.Paths) map[string]string {
	dirs := []string{"/tmp", paths.Workspace}
	if paths.Home != "" {
		dirs = append(dirs, filepath.Join(paths.Home, "go"), filepath.Join(paths.Home, ".cache"))
	}
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
// default injected paths merged with the user-configured permissions.paths.
// User entries override defaults for the same path.
func (s *Service) PermissionPaths(paths sandboxpath.Paths) map[string]string {
	result := defaultPermissionPaths(paths)
	if s == nil {
		return result
	}
	for pattern, mode := range s.config.Permission.Paths {
		result[pattern] = mode
	}
	return result
}

func (s *Service) MountProfiles() sandboxmount.Profiles {
	if s == nil {
		return nil
	}
	return s.config.MountProfiles.Clone()
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

func (s *Service) Sandbox() SandboxConfig {
	if s == nil {
		return SandboxConfig{}
	}
	return s.config.Sandbox
}

func (s *Service) MCPSandbox() MCPSandboxConfig {
	if s == nil {
		return MCPSandboxConfig{}
	}
	return s.config.Sandbox.MCP
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

func (s MCPServer) Runtime() MCPRuntimeConfig {
	return rawMCPRuntimeFromAny(s.raw["runtime"])
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

func rawMCPRuntimeFromAny(raw any) MCPRuntimeConfig {
	switch value := raw.(type) {
	case string:
		return MCPRuntimeConfig{Type: strings.TrimSpace(value)}
	case map[string]any:
		runtime := MCPRuntimeConfig{}
		if typ, ok := value["type"].(string); ok {
			runtime.Type = strings.TrimSpace(typ)
		}
		if docker, ok := value["docker"].(map[string]any); ok {
			if image, ok := docker["image"].(string); ok {
				runtime.Docker.Image = strings.TrimSpace(image)
			}
		}
		return runtime
	default:
		return MCPRuntimeConfig{}
	}
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
