package mcpconfig

// Decodes direct `resources.mcps` maps into the native MCP contract.

import (
	"errors"
	"fmt"

	configfile "petris.dev/toby/internal/config/file"
)

// Skipped is one MCP server that was omitted because its definition is invalid.
type Skipped struct {
	Name string
	Err  error
}

// DecodeWithSubstitutions strictly decodes one direct `resources.mcps` map,
// resolves host-owned substitutions, and validates the effective contract.
// Invalid optional servers are omitted and returned as Skipped entries.
// Reserved names, substitution failures, and the server-count limit remain
// fail-closed.
func DecodeWithSubstitutions(
	block map[string]any,
	resolve func(string) (string, error),
) (Config, []Skipped, error) {
	if resolve == nil {
		return Config{}, nil, fmt.Errorf("MCP substitution resolver is required")
	}
	if len(block) > maxServers {
		return Config{}, nil, fmt.Errorf(
			"resources.mcps count exceeds %d",
			maxServers,
		)
	}
	if _, exists := block[builtinServerName]; exists {
		return Config{}, nil, fmt.Errorf(
			"resources.mcps.%s is reserved for Toby",
			builtinServerName,
		)
	}

	result := Config{
		Servers: make(map[string]Server, len(block)),
	}
	var skipped []Skipped
	for _, name := range sortedNames(block) {
		if err := validateServerName(name); err != nil {
			skipped = append(skipped, Skipped{Name: name, Err: err})
			continue
		}
		server, err := decodeServer(name, block[name], resolve)
		if err != nil {
			var substitution *SubstitutionError
			if errors.As(err, &substitution) {
				return Config{}, nil, err
			}
			skipped = append(skipped, Skipped{Name: name, Err: err})
			continue
		}
		result.Servers[name] = server
	}

	return result, skipped, nil
}

func decodeServer(
	name string,
	raw any,
	resolve func(string) (string, error),
) (Server, error) {
	serverMap, ok := raw.(map[string]any)
	if !ok {
		return Server{}, fmt.Errorf(
			"resources.mcps.%s must be an object",
			name,
		)
	}
	if err := validateServerRawFields(name, serverMap); err != nil {
		return Server{}, err
	}

	var schema serverSchema
	if err := configfile.DecodeInto(serverMap, &schema); err != nil {
		return Server{}, fmt.Errorf(
			"decode resources.mcps.%s: %w",
			name,
			err,
		)
	}
	if schema.IdleTimeout != nil && schema.IdleTimeout.Duration <= 0 {
		return Server{}, fmt.Errorf(
			"resources.mcps.%s.idleTimeout must be positive",
			name,
		)
	}

	server := schema.server()
	if server.Type == ServerLocal &&
		server.Transport == TransportHTTP &&
		server.IdleTimeout == 0 {
		server.IdleTimeout = DefaultIdleTimeout
	}

	resolved, err := resolveServerSubstitutions(name, server, resolve)
	if err != nil {
		return Server{}, err
	}
	if err := resolved.validate(name); err != nil {
		return Server{}, err
	}

	return resolved, nil
}

func validateServerRawFields(name string, server map[string]any) error {
	typ, _ := server["type"].(string)
	transport, _ := server["transport"].(string)
	if typ == string(ServerRemote) {
		for _, field := range []string{
			"image",
			"command",
			"environment",
			"endpoint",
			"mounts",
			"scope",
			"network",
			"idleTimeout",
		} {
			if _, exists := server[field]; exists {
				return fmt.Errorf(
					"resources.mcps.%s: remote server contains local-only field %q",
					name,
					field,
				)
			}
		}
	}
	if typ == string(ServerLocal) {
		for _, field := range []string{"url", "headers"} {
			if _, exists := server[field]; exists {
				return fmt.Errorf(
					"resources.mcps.%s: local server contains remote-only field %q",
					name,
					field,
				)
			}
		}
	}
	if transport == string(TransportStdio) {
		for _, field := range []string{"endpoint", "idleTimeout"} {
			if _, exists := server[field]; exists {
				return fmt.Errorf(
					"resources.mcps.%s: stdio server must not define %s",
					name,
					field,
				)
			}
		}
	}

	return nil
}
