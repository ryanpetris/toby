package toolconfig

import (
	"bytes"
	"fmt"

	"petris.dev/toby/internal/configfile"
)

func CommandParts(name string, raw any) (string, []string, error) {
	switch command := raw.(type) {
	case string:
		if command == "" {
			return "", nil, fmt.Errorf("mcp server %q command is empty", name)
		}
		return command, nil, nil
	case []any:
		if len(command) == 0 {
			return "", nil, fmt.Errorf("mcp server %q command is empty", name)
		}
		first, ok := command[0].(string)
		if !ok || first == "" {
			return "", nil, fmt.Errorf("mcp server %q command must start with a string", name)
		}
		args := make([]string, 0, len(command)-1)
		for _, item := range command[1:] {
			arg, ok := item.(string)
			if !ok {
				return "", nil, fmt.Errorf("mcp server %q command arguments must be strings", name)
			}
			args = append(args, arg)
		}
		return first, args, nil
	default:
		return "", nil, fmt.Errorf("mcp server %q command is required", name)
	}
}

func CopyField(dst, src map[string]any, from, to string) {
	if value, ok := src[from]; ok {
		dst[to] = configfile.Clone(value)
	}
}

func CopyEnvVars(dst, src map[string]any) {
	raw, ok := src["env_vars"]
	if !ok {
		return
	}
	env := StringMap(dst["env"])
	for _, name := range StringList(raw) {
		if _, exists := env[name]; !exists {
			env[name] = "${" + name + "}"
		}
	}
	if len(env) > 0 {
		dst["env"] = env
	}
}

func CopyEnvHeaders(dst, src map[string]any) {
	raw, ok := src["env_http_headers"]
	if !ok {
		return
	}
	headers := StringMap(dst["headers"])
	for name, value := range StringMap(raw) {
		envName, ok := value.(string)
		if !ok || envName == "" {
			continue
		}
		if _, exists := headers[name]; !exists {
			headers[name] = "${" + envName + "}"
		}
	}
	if len(headers) > 0 {
		dst["headers"] = headers
	}
}

func CopyBearerTokenEnv(dst, src map[string]any) {
	name, _ := src["bearer_token_env_var"].(string)
	if name == "" {
		return
	}
	headers := StringMap(dst["headers"])
	if _, exists := headers["Authorization"]; !exists {
		headers["Authorization"] = "Bearer ${" + name + "}"
	}
	dst["headers"] = headers
}

func StringMap(raw any) map[string]any {
	result := map[string]any{}
	switch values := raw.(type) {
	case map[string]any:
		for key, value := range values {
			result[key] = configfile.Clone(value)
		}
	case map[string]string:
		for key, value := range values {
			result[key] = value
		}
	}
	return result
}

func StringList(raw any) []string {
	switch values := raw.(type) {
	case []any:
		items := make([]string, 0, len(values))
		for _, value := range values {
			if item, ok := value.(string); ok && item != "" {
				items = append(items, item)
			}
		}
		return items
	case []string:
		return append([]string(nil), values...)
	default:
		return nil
	}
}

func JoinInstructions(instructions [][]byte) []byte {
	parts := make([][]byte, 0, len(instructions))
	for _, item := range instructions {
		if len(bytes.TrimSpace(item)) == 0 {
			continue
		}
		parts = append(parts, bytes.TrimRight(item, "\n"))
	}
	if len(parts) == 0 {
		return nil
	}
	return append(bytes.Join(parts, []byte("\n\n")), '\n')
}

func JoinInstructionsString(instructions [][]byte) string {
	return string(JoinInstructions(instructions))
}

func JoinInstructionsOrNewline(instructions [][]byte) []byte {
	joined := JoinInstructions(instructions)
	if joined != nil {
		return joined
	}
	return []byte("\n")
}
