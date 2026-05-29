package tobyconfig

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/configfile"
	"petris.dev/toby/internal/contextfiles"
)

const InstructionsDir = "instructions"

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
}

type MCPServer struct {
	raw map[string]any
}

type ProviderConfig struct {
	raw map[string]any
}

type PermissionConfig struct {
	ExternalDirectory map[string]string
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
			ExternalDirectory: map[string]string{},
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
		parsed, err := parseConfig(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		merged.Merge(parsed)
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

func parseConfig(raw map[string]any) (Config, error) {
	result := Config{
		MCP:      map[string]MCPServer{},
		Provider: map[string]ProviderConfig{},
		Permission: PermissionConfig{
			ExternalDirectory: map[string]string{},
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
			permission, err := parsePermission(value)
			if err != nil {
				return Config{}, err
			}
			result.Permission = permission
		case "provider":
			providers, err := parseObjectMap("provider", value)
			if err != nil {
				return Config{}, err
			}
			for name, provider := range providers {
				result.Provider[name] = ProviderConfig{raw: provider}
			}
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
			merged := existing.Raw()
			configfile.Merge(merged, provider.Raw())
			c.Provider[name] = ProviderConfig{raw: merged}
			continue
		}
		c.Provider[name] = ProviderConfig{raw: provider.Raw()}
	}
	if c.Permission.ExternalDirectory == nil {
		c.Permission.ExternalDirectory = map[string]string{}
	}
	for pattern, mode := range src.Permission.ExternalDirectory {
		c.Permission.ExternalDirectory[pattern] = mode
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
	for name, server := range s.config.MCP {
		servers[name] = MCPServer{raw: server.Raw()}
	}
	return servers
}

func (s *Service) Providers() map[string]ProviderConfig {
	providers := map[string]ProviderConfig{}
	if s == nil {
		return providers
	}
	for name, provider := range s.config.Provider {
		providers[name] = ProviderConfig{raw: provider.Raw()}
	}
	return providers
}

func (s *Service) Permission() PermissionConfig {
	permission := PermissionConfig{ExternalDirectory: map[string]string{}}
	if s == nil {
		return permission
	}
	for pattern, mode := range s.config.Permission.ExternalDirectory {
		permission.ExternalDirectory[pattern] = mode
	}
	return permission
}

func (s *Service) RegisterContextFiles(session *contextfiles.Session) error {
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
		if err := session.AddInstructionBytes(rel, data, 0o400); err != nil {
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

func (p ProviderConfig) Raw() map[string]any {
	return configfile.CloneMap(p.raw)
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

func parsePermission(raw any) (PermissionConfig, error) {
	permission := PermissionConfig{ExternalDirectory: map[string]string{}}
	if raw == nil {
		return permission, nil
	}
	items, ok := raw.(map[string]any)
	if !ok {
		return PermissionConfig{}, fmt.Errorf("permission must be an object")
	}
	for key, value := range items {
		if key != "external_directory" {
			return PermissionConfig{}, fmt.Errorf("unsupported permission key %q", key)
		}
		external, ok := value.(map[string]any)
		if !ok {
			return PermissionConfig{}, fmt.Errorf("permission.external_directory must be an object")
		}
		for pattern, rawMode := range external {
			mode, ok := rawMode.(string)
			if !ok {
				return PermissionConfig{}, fmt.Errorf("permission.external_directory[%q] must be a string", pattern)
			}
			permission.ExternalDirectory[pattern] = mode
		}
	}
	return permission, nil
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
