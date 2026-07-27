package mcpconfig

// Decodes direct `resources.mcps` maps into the native MCP contract.

import (
	"fmt"

	configfile "petris.dev/toby/internal/config/file"
)

// DecodeWithSubstitutions strictly decodes one direct `resources.mcps` map,
// resolves host-owned substitutions, and validates the effective contract.
func DecodeWithSubstitutions(
	block map[string]any,
	resolve func(string) (string, error),
) (Config, error) {
	schema, err := decodeSchema(block)
	if err != nil {
		return Config{}, err
	}
	config, err := schema.build()
	if err != nil {
		return Config{}, err
	}

	return config.ResolveSubstitutions(resolve)
}

func decodeSchema(block map[string]any) (blockSchema, error) {
	if err := validateRawFieldPresence(block); err != nil {
		return blockSchema{}, err
	}

	var schema blockSchema
	if err := configfile.DecodeInto(
		map[string]any{"servers": block},
		&schema,
	); err != nil {
		return blockSchema{}, fmt.Errorf(
			"decode resources.mcps: %w",
			err,
		)
	}

	return schema, nil
}

func validateRawFieldPresence(block map[string]any) error {
	for _, name := range sortedNames(block) {
		value := block[name]
		server, ok := value.(map[string]any)
		if !ok {
			continue
		}

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
	}

	return nil
}
