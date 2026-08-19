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
	"petris.dev/toby/internal/diagnostic/warning"
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
// input-only resources block. Callers invoke this only after hello. Invalid
// optional MCP servers and models endpoints are skipped and warned.
func (s *Service) ResolveResources(
	warnings *warning.Service,
) (ResourcesConfig, error) {
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

	mcps, skipped, err := mcpconfig.DecodeWithSubstitutions(
		configfile.CloneMap(schema.MCPs),
		func(value string) (string, error) {
			configDirs, home := s.resolutionContext()
			return resolveString(value, configDirs, home)
		},
	)
	if err != nil {
		return ResourcesConfig{}, err
	}
	for _, item := range skipped {
		warnSkippedResource(
			warnings,
			warning.MCPServerInvalid,
			fmt.Sprintf(
				"MCP server %q is invalid; skipping it: %v",
				item.Name,
				item.Err,
			),
			item.Err,
			"mcp_server", item.Name,
		)
	}

	models, err := s.resolveModels(schema.Models, warnings)
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
	warnings *warning.Service,
) (map[string]modelsconfig.Config, error) {
	result := make(map[string]modelsconfig.Config, len(raw))
	names := make([]string, 0, len(raw))
	for name := range raw {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		value := raw[name]
		object, ok := value.(map[string]any)
		if !ok {
			warnSkippedResource(
				warnings,
				warning.ModelsEndpointUnavailable,
				fmt.Sprintf(
					"models endpoint %q is invalid; skipping it: must be an object",
					name,
				),
				fmt.Errorf("resources.models.%s must be an object", name),
				"models_endpoint", name,
			)
			continue
		}
		var model modelSchema
		if err := configfile.DecodeInto(object, &model); err != nil {
			warnSkippedResource(
				warnings,
				warning.ModelsEndpointUnavailable,
				fmt.Sprintf(
					"models endpoint %q is invalid; skipping it: %v",
					name,
					err,
				),
				err,
				"models_endpoint", name,
			)
			continue
		}
		effective, err := model.resolve(name)
		if err != nil {
			warnSkippedResource(
				warnings,
				warning.ModelsEndpointUnavailable,
				fmt.Sprintf(
					"models endpoint %q is invalid; skipping it: %v",
					name,
					err,
				),
				err,
				"models_endpoint", name,
			)
			continue
		}
		result[name] = effective
	}

	return result, nil
}

func warnSkippedResource(
	warnings *warning.Service,
	id warning.ID,
	message string,
	err error,
	attributes ...any,
) {
	if warnings == nil {
		return
	}
	if err != nil {
		warnings.WarnError(id, message, err, attributes...)
		return
	}
	warnings.Warn(id, message, attributes...)
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
