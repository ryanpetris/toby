package appconfig

// Delays strict MCP and models resolution until a launch has established its
// agent session.

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	configfile "petris.dev/toby/internal/config/file"
	mcpconfig "petris.dev/toby/internal/config/mcp"
	modelsconfig "petris.dev/toby/internal/config/models"
)

// ResourcesConfig is the detached effective resource configuration for one
// launch.
type ResourcesConfig struct {
	MCPs   map[string]mcpconfig.Server
	Models map[string]modelsconfig.Config
}

type resourceSchema struct {
	MCPs   map[string]any `json:"mcps" yaml:"mcps"`
	Models map[string]any `json:"models" yaml:"models"`
}

type modelSchema struct {
	Protocol string            `json:"protocol" yaml:"protocol"`
	Name     string            `json:"name" yaml:"name"`
	URL      string            `json:"url" yaml:"url"`
	Headers  map[string]string `json:"headers" yaml:"headers"`
}

// ResolveResources strictly defaults, substitutes, and validates the
// input-only resources block. Callers invoke this only after hello.
func (s *Service) ResolveResources() (ResourcesConfig, error) {
	if s == nil {
		return emptyResources(), nil
	}

	rawResources := configfile.CloneMap(s.rawResources)
	for _, name := range []string{"mcps", "models"} {
		value, present := rawResources[name]
		if !present {
			continue
		}
		if _, ok := value.(map[string]any); !ok {
			return ResourcesConfig{}, fmt.Errorf(
				"%s: resources.%s must be an object",
				s.Dir,
				name,
			)
		}
	}

	var schema resourceSchema
	if err := configfile.DecodeInto(
		rawResources,
		&schema,
	); err != nil {
		return ResourcesConfig{}, fmt.Errorf(
			"%s: decode resources: %w",
			s.Dir,
			err,
		)
	}

	mcps, err := mcpconfig.DecodeWithSubstitutions(
		configfile.CloneMap(schema.MCPs),
		func(value string) (string, error) {
			configDirs, home := s.resolutionContext()
			return resolveString(value, configDirs, home)
		},
	)
	if err != nil {
		return ResourcesConfig{}, err
	}

	models, err := s.resolveModels(schema.Models)
	if err != nil {
		return ResourcesConfig{}, err
	}

	return ResourcesConfig{
		MCPs:   cloneMCPServers(mcps.Servers),
		Models: models,
	}, nil
}

func (s *Service) resolveModels(
	raw map[string]any,
) (map[string]modelsconfig.Config, error) {
	decoded := make(map[string]*modelSchema)
	if err := configfile.DecodeInto(
		configfile.CloneMap(raw),
		&decoded,
	); err != nil {
		return nil, fmt.Errorf("decode resources.models: %w", err)
	}

	result := make(map[string]modelsconfig.Config, len(decoded))
	names := make([]string, 0, len(decoded))
	for name := range decoded {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		model := decoded[name]
		if model == nil {
			return nil, fmt.Errorf(
				"resources.models.%s must be an object",
				name,
			)
		}
		effective, err := model.resolve(name)
		if err != nil {
			return nil, err
		}
		result[name] = effective
	}

	return result, nil
}

func (m *modelSchema) resolve(
	name string,
) (modelsconfig.Config, error) {
	result := modelsconfig.Config{
		Protocol: modelsconfig.Protocol(strings.TrimSpace(m.Protocol)),
		Name:     m.Name,
		URL:      strings.TrimSpace(m.URL),
	}
	if result.Protocol == "" {
		return modelsconfig.Config{}, fmt.Errorf(
			"resources.models.%s.protocol is required",
			name,
		)
	}
	if !modelProtocolSupported(result.Protocol) {
		return modelsconfig.Config{}, fmt.Errorf(
			"resources.models.%s.protocol is unsupported",
			name,
		)
	}
	if result.URL == "" {
		return modelsconfig.Config{}, fmt.Errorf(
			"resources.models.%s.url is required",
			name,
		)
	}
	if len(m.Headers) > 0 {
		result.Headers = make(map[string]string, len(m.Headers))
		for key, value := range m.Headers {
			result.Headers[key] = value
		}
	}
	return modelsconfig.Normalize(result)
}

// ResolveModelHeaders resolves protected host substitutions for one models
// resource after the agent session exists.
func (s *Service) ResolveModelHeaders(
	name string,
	model modelsconfig.Config,
) (http.Header, error) {
	configDirs, home := s.resolutionContext()
	headers := http.Header{}
	for key, value := range model.Headers {
		resolved, err := resolveString(value, configDirs, home)
		if err != nil {
			return nil, fmt.Errorf(
				"models resource %q header %q: %w",
				name,
				key,
				err,
			)
		}
		headers.Set(key, resolved)
	}

	return headers, nil
}

func emptyResources() ResourcesConfig {
	return ResourcesConfig{
		MCPs:   make(map[string]mcpconfig.Server),
		Models: make(map[string]modelsconfig.Config),
	}
}

func cloneMCPServers(
	servers map[string]mcpconfig.Server,
) map[string]mcpconfig.Server {
	return mcpconfig.Config{
		Servers: servers,
	}.Clone().Servers
}

func modelProtocolSupported(protocol modelsconfig.Protocol) bool {
	switch protocol {
	case modelsconfig.ProtocolAnthropic, modelsconfig.ProtocolOpenAI:
		return true
	default:
		return false
	}
}
