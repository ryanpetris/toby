package grokconfig

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"petris.dev/toby/internal/configfile"
	"petris.dev/toby/internal/contextfiles"
	"petris.dev/toby/internal/httpproxy"
	"petris.dev/toby/internal/proxyconfig"
	"petris.dev/toby/internal/tobyconfig"
)

const StaticConfigPath = "grok/config.toml"

func RegisterContextFiles(registrar contextfiles.Registrar, _ [][]byte, cfg *tobyconfig.Service, controlHost, tobyMCPURL string, proxy *httpproxy.Service) error {
	config, err := syntheticConfig(cfg, controlHost, tobyMCPURL, proxy)
	if err != nil {
		return err
	}
	return registrar.AddBytes(StaticConfigPath, config, 0o400)
}

func ConfigPath(contextDir string) string {
	return filepath.Join(contextDir, filepath.FromSlash(StaticConfigPath))
}

func Rules(instructions [][]byte) string {
	return joinInstructions(instructions)
}

func syntheticConfig(cfg *tobyconfig.Service, controlHost, tobyMCPURL string, proxy *httpproxy.Service) ([]byte, error) {
	servers, err := syntheticMCPServers(cfg, controlHost, tobyMCPURL, proxy)
	if err != nil {
		return nil, err
	}
	return marshalConfig(servers)
}

func syntheticMCPServers(cfg *tobyconfig.Service, controlHost, tobyMCPURL string, proxy *httpproxy.Service) (map[string]map[string]any, error) {
	servers := map[string]map[string]any{}
	if cfg != nil {
		for name, configured := range cfg.MCPServers() {
			if !configured.Enabled() {
				continue
			}
			if configured.HTTPProxyable() {
				converted, err := syntheticProxyMCP(controlHost, proxy, name, configured)
				if err != nil {
					return nil, err
				}
				servers[name] = converted
				continue
			}
			raw := configured.Raw()
			converted, err := convertMCPServer(name, raw)
			if err != nil {
				return nil, err
			}
			servers[name] = converted
		}
	}
	toby, err := syntheticTobyMCP(tobyMCPURL)
	if err != nil {
		return nil, err
	}
	servers["toby"] = toby
	return servers, nil
}

func syntheticTobyMCP(url string) (map[string]any, error) {
	if strings.TrimSpace(url) == "" {
		return nil, fmt.Errorf("toby MCP proxy URL is required")
	}
	return map[string]any{
		"url":     strings.TrimSpace(url),
		"enabled": true,
	}, nil
}

func syntheticProxyMCP(controlHost string, proxy *httpproxy.Service, name string, server tobyconfig.MCPServer) (map[string]any, error) {
	proxyURL, err := proxyconfig.MCPURL(controlHost, proxy, server)
	if err != nil {
		return nil, fmt.Errorf("mcp.%s: %w", name, err)
	}
	converted := map[string]any{
		"url":     proxyURL,
		"enabled": true,
	}
	copyCommonFields(converted, server.Raw())
	return converted, nil
}

func convertMCPServer(name string, server map[string]any) (map[string]any, error) {
	typ, _ := server["type"].(string)
	switch typ {
	case "":
		if _, ok := server["command"]; ok {
			return convertLocalMCPServer(name, server)
		}
		if _, ok := server["url"]; ok {
			return convertRemoteMCPServer(name, "", server)
		}
		return nil, fmt.Errorf("mcp server %q command or url is required", name)
	case "local":
		return convertLocalMCPServer(name, server)
	case "remote":
		return convertRemoteMCPServer(name, "", server)
	default:
		return nil, fmt.Errorf("unsupported Grok mcp server %q type %q", name, typ)
	}
}

func convertLocalMCPServer(name string, server map[string]any) (map[string]any, error) {
	command, args, err := commandParts(name, server["command"])
	if err != nil {
		return nil, err
	}
	converted := map[string]any{"command": command}
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
	converted := map[string]any{"url": url}
	if typ != "" {
		converted["type"] = typ
	}
	copyCommonFields(converted, server)
	copyField(converted, server, "headers", "headers")
	copyField(converted, server, "http_headers", "headers")
	copyEnvHeaders(converted, server)
	copyBearerTokenEnv(converted, server)
	return converted, nil
}

func copyCommonFields(dst, src map[string]any) {
	for _, key := range []string{"enabled", "startup_timeout_sec", "tool_timeout_sec", "tool_timeouts"} {
		copyField(dst, src, key, key)
	}
	copyField(dst, src, "timeout", "startup_timeout_sec")
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

func joinInstructions(instructions [][]byte) string {
	parts := make([][]byte, 0, len(instructions))
	for _, item := range instructions {
		if len(bytes.TrimSpace(item)) == 0 {
			continue
		}
		parts = append(parts, bytes.TrimRight(item, "\n"))
	}
	if len(parts) == 0 {
		return ""
	}
	return string(append(bytes.Join(parts, []byte("\n\n")), '\n'))
}

func marshalConfig(servers map[string]map[string]any) ([]byte, error) {
	return toml.Marshal(struct {
		MCPServers map[string]map[string]any `toml:"mcp_servers"`
	}{MCPServers: servers})
}
