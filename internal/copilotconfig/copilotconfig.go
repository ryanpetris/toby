package copilotconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"

	"petris.dev/toby/internal/configfile"
	"petris.dev/toby/internal/contextfiles"
	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/tobyconfig"
)

const (
	StaticMCPPath          = "copilot/mcp-config.json"
	StaticInstructionsPath = "copilot/AGENTS.md"
)

func RegisterContextFiles(registrar contextfiles.Registrar, instructions [][]byte, cfg *tobyconfig.Service) error {
	mcpConfig, err := syntheticMCP(cfg)
	if err != nil {
		return err
	}
	mcp, err := marshalJSON(mcpConfig)
	if err != nil {
		return err
	}
	if err := registrar.AddBytes(StaticMCPPath, mcp, 0o400); err != nil {
		return err
	}
	return registrar.AddBytes(StaticInstructionsPath, joinInstructions(instructions), 0o400)
}

func MCPConfigPath(contextDir string) string {
	return filepath.Join(contextDir, filepath.FromSlash(StaticMCPPath))
}

func InstructionsDir(contextDir string) string {
	return filepath.Join(contextDir, "copilot")
}

func syntheticMCP(cfg *tobyconfig.Service) (map[string]any, error) {
	servers := map[string]any{}
	if cfg != nil {
		for name, configured := range cfg.MCPServers() {
			if !configured.Enabled() {
				continue
			}
			converted, err := convertMCPServer(name, configured.Raw())
			if err != nil {
				return nil, err
			}
			servers[name] = converted
		}
	}
	servers["toby"] = syntheticTobyMCP()
	return map[string]any{"mcpServers": servers}, nil
}

func syntheticTobyMCP() map[string]any {
	return map[string]any{
		"type":    "stdio",
		"command": "toby",
		"args":    []any{"sandbox", "mcp"},
		"env": map[string]any{
			control.EnvControlURL:   "${" + control.EnvControlURL + "}",
			control.EnvControlToken: "${" + control.EnvControlToken + "}",
		},
		"tools": []any{"*"},
	}
}

func convertMCPServer(name string, server map[string]any) (map[string]any, error) {
	typ, _ := server["type"].(string)
	switch typ {
	case "":
		if _, ok := server["command"]; ok {
			return convertLocalMCPServer(name, server)
		}
		if _, ok := server["url"]; ok {
			return convertRemoteMCPServer(name, "http", server)
		}
		return nil, fmt.Errorf("mcp server %q command or url is required", name)
	case "local", "stdio":
		return convertLocalMCPServer(name, server)
	case "remote", "http", "streamable-http":
		return convertRemoteMCPServer(name, "http", server)
	case "sse":
		return convertRemoteMCPServer(name, "sse", server)
	default:
		return nil, fmt.Errorf("unsupported Copilot mcp server %q type %q", name, typ)
	}
}

func convertLocalMCPServer(name string, server map[string]any) (map[string]any, error) {
	command, args, err := commandParts(name, server["command"])
	if err != nil {
		return nil, err
	}
	converted := map[string]any{"type": "stdio", "command": command}
	if len(args) > 0 {
		converted["args"] = args
	}
	copyCommonFields(converted, server)
	copyField(converted, server, "env", "env")
	copyField(converted, server, "environment", "env")
	copyEnvVars(converted, server)
	copyField(converted, server, "cwd", "cwd")
	return converted, nil
}

func convertRemoteMCPServer(name, typ string, server map[string]any) (map[string]any, error) {
	url, ok := server["url"].(string)
	if !ok || url == "" {
		return nil, fmt.Errorf("mcp server %q url is required", name)
	}
	converted := map[string]any{"type": typ, "url": url}
	copyCommonFields(converted, server)
	copyField(converted, server, "headers", "headers")
	copyField(converted, server, "http_headers", "headers")
	copyEnvHeaders(converted, server)
	copyBearerTokenEnv(converted, server)
	return converted, nil
}

func copyCommonFields(dst, src map[string]any) {
	for _, key := range []string{"enabled", "tools"} {
		copyField(dst, src, key, key)
	}
}

func commandParts(name string, raw any) (string, []any, error) {
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
		args := make([]any, 0, len(command)-1)
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

func copyField(dst, src map[string]any, from, to string) {
	if value, ok := src[from]; ok {
		dst[to] = configfile.Clone(value)
	}
}

func copyEnvVars(dst, src map[string]any) {
	raw, ok := src["env_vars"]
	if !ok {
		return
	}
	env := stringMap(dst["env"])
	for _, name := range stringList(raw) {
		if _, exists := env[name]; !exists {
			env[name] = "${" + name + "}"
		}
	}
	if len(env) > 0 {
		dst["env"] = env
	}
}

func copyEnvHeaders(dst, src map[string]any) {
	raw, ok := src["env_http_headers"]
	if !ok {
		return
	}
	headers := stringMap(dst["headers"])
	for name, value := range stringMap(raw) {
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

func copyBearerTokenEnv(dst, src map[string]any) {
	name, _ := src["bearer_token_env_var"].(string)
	if name == "" {
		return
	}
	headers := stringMap(dst["headers"])
	if _, exists := headers["Authorization"]; !exists {
		headers["Authorization"] = "Bearer ${" + name + "}"
	}
	dst["headers"] = headers
}

func stringMap(raw any) map[string]any {
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

func stringList(raw any) []string {
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

func joinInstructions(instructions [][]byte) []byte {
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

func marshalJSON(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
