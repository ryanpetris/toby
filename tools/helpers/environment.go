package helpers

// Environment construction from KEY=VALUE string lists.

import (
	"strings"

	"petris.dev/toby/platform/environ"
)

func EnvironmentFromList(values []string) environ.Environment {
	env := make(environ.Environment, len(values))
	for _, item := range values {
		name, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		env[name] = value
	}
	return env
}
