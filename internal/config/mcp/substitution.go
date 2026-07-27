package mcpconfig

// Resolves host-owned substitutions before secret-bearing MCP configuration is
// sent to the per-user agent.

import "fmt"

// ResolveSubstitutions returns a detached configuration with substitutions
// resolved in remote URLs and headers and in local commands and environment
// values. The resolver runs on the host; its outputs are validated before the
// configuration is returned.
func (c Config) ResolveSubstitutions(
	resolve func(string) (string, error),
) (Config, error) {
	if resolve == nil {
		return Config{}, fmt.Errorf("MCP substitution resolver is required")
	}

	resolved := c.Clone()
	for _, name := range sortedNames(resolved.Servers) {
		server := resolved.Servers[name]

		value, err := resolve(server.URL)
		if err != nil {
			return Config{}, fmt.Errorf(
				"resources.mcps.%s.url: %w",
				name,
				err,
			)
		}
		server.URL = value

		for _, header := range sortedNames(server.Headers) {
			raw := server.Headers[header]
			value, err := resolve(raw)
			if err != nil {
				return Config{}, fmt.Errorf(
					"resources.mcps.%s.headers.%s: %w",
					name,
					header,
					err,
				)
			}
			server.Headers[header] = value
		}
		for index, raw := range server.Command {
			value, err := resolve(raw)
			if err != nil {
				return Config{}, fmt.Errorf(
					"resources.mcps.%s.command[%d]: %w",
					name,
					index,
					err,
				)
			}
			server.Command[index] = value
		}
		for _, environment := range sortedNames(server.Environment) {
			raw := server.Environment[environment]
			value, err := resolve(raw)
			if err != nil {
				return Config{}, fmt.Errorf(
					"resources.mcps.%s.environment.%s: %w",
					name,
					environment,
					err,
				)
			}
			server.Environment[environment] = value
		}

		resolved.Servers[name] = server
	}

	if err := resolved.Validate(); err != nil {
		return Config{}, err
	}
	return resolved, nil
}
