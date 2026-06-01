package helpers

import (
	"strings"

	"petris.dev/toby/internal/tools/tool"
)

func EnvironmentFromList(values []string) tool.Environment {
	env := make(tool.Environment, len(values))
	for _, item := range values {
		name, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		env[name] = value
	}
	return env
}
