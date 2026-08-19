package mcpconfig

// Resolves host-owned substitutions before secret-bearing MCP configuration is
// sent to the per-user agent.

import (
	"fmt"
	"strconv"
)

// SubstitutionError is a fail-closed host substitution failure for one MCP
// credential-bearing field.
type SubstitutionError struct {
	Server string
	Field  string
	Err    error
}

func (e *SubstitutionError) Error() string {
	if e == nil {
		return "MCP substitution error"
	}
	return fmt.Sprintf("resources.mcps.%s.%s: %v", e.Server, e.Field, e.Err)
}

func (e *SubstitutionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func resolveServerSubstitutions(
	name string,
	server Server,
	resolve func(string) (string, error),
) (Server, error) {
	value, err := resolve(server.URL)
	if err != nil {
		return Server{}, &SubstitutionError{
			Server: name,
			Field:  "url",
			Err:    err,
		}
	}
	server.URL = value

	for _, header := range sortedNames(server.Headers) {
		raw := server.Headers[header]
		value, err := resolve(raw)
		if err != nil {
			return Server{}, &SubstitutionError{
				Server: name,
				Field:  "headers." + header,
				Err:    err,
			}
		}
		server.Headers[header] = value
	}
	for index, raw := range server.Command {
		value, err := resolve(raw)
		if err != nil {
			return Server{}, &SubstitutionError{
				Server: name,
				Field:  "command[" + strconv.Itoa(index) + "]",
				Err:    err,
			}
		}
		server.Command[index] = value
	}
	for _, environment := range sortedNames(server.Environment) {
		raw := server.Environment[environment]
		value, err := resolve(raw)
		if err != nil {
			return Server{}, &SubstitutionError{
				Server: name,
				Field:  "environment." + environment,
				Err:    err,
			}
		}
		server.Environment[environment] = value
	}

	return server, nil
}
