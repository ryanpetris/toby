// Package claudeconfig generates the synthetic Claude Code configuration that
// Toby writes into the sandbox runtime context directory. Unlike OpenCode, Claude Code
// writes runtime state (credentials, history, transcripts) into its config
// directory, so Toby leaves Claude's config directory on normal tool state.
// The generated files here are passed to Claude via launch flags
// (--mcp-config, --settings, --append-system-prompt-file), which
// achieves the same injection OpenCode gets from its merged opencode.json.
package claudeconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"petris.dev/toby/internal/configfile"
	"petris.dev/toby/internal/contextfiles"
	"petris.dev/toby/internal/httpproxy"
	"petris.dev/toby/internal/proxyconfig"
	"petris.dev/toby/internal/tobyconfig"
)

const (
	// StaticMcpPath holds the toby MCP server definition (--mcp-config).
	StaticMcpPath = "claude/mcp.json"
	// StaticSettingsPath holds generated Claude settings (--settings).
	StaticSettingsPath = "claude/settings.json"
	// StaticInstructionsPath holds the combined instruction text
	// (--append-system-prompt-file).
	StaticInstructionsPath = "claude/instructions.md"
)

// RegisterContextFiles renders the Claude Code synthetic configuration files.
// instructions is the content of Toby's instruction files; they are concatenated
// into a single file so the launcher can pass exactly one
// --append-system-prompt-file.
func RegisterContextFiles(registrar contextfiles.Registrar, projectRoot string, instructions [][]byte, cfg *tobyconfig.Service, controlHost, tobyMCPURL string, proxy *httpproxy.Service) error {
	mcpConfig, err := syntheticMCP(cfg, controlHost, tobyMCPURL, proxy)
	if err != nil {
		return err
	}
	mcp, err := marshalJSON(mcpConfig)
	if err != nil {
		return err
	}
	settings, err := marshalJSON(syntheticSettings())
	if err != nil {
		return err
	}
	if err := registrar.AddBytes(StaticMcpPath, mcp, 0o400); err != nil {
		return err
	}
	if err := registrar.AddBytes(StaticSettingsPath, settings, 0o400); err != nil {
		return err
	}
	if err := registrar.AddBytes(StaticInstructionsPath, joinInstructions(instructions), 0o400); err != nil {
		return err
	}
	return nil
}

func syntheticMCP(cfg *tobyconfig.Service, controlHost, tobyMCPURL string, proxy *httpproxy.Service) (map[string]any, error) {
	servers := map[string]any{}
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
	return map[string]any{"mcpServers": servers}, nil
}

func syntheticTobyMCP(url string) (map[string]any, error) {
	if strings.TrimSpace(url) == "" {
		return nil, fmt.Errorf("toby MCP proxy URL is required")
	}
	return map[string]any{
		"type": "http",
		"url":  strings.TrimSpace(url),
	}, nil
}

func syntheticProxyMCP(controlHost string, proxy *httpproxy.Service, name string, server tobyconfig.MCPServer) (map[string]any, error) {
	proxyURL, err := proxyconfig.MCPURL(controlHost, proxy, server)
	if err != nil {
		return nil, fmt.Errorf("mcp.%s: %w", name, err)
	}
	converted := map[string]any{
		"type": "http",
		"url":  proxyURL,
	}
	raw := server.Raw()
	copyField(converted, raw, "enabled", "enabled")
	copyField(converted, raw, "timeout", "timeout")
	copyField(converted, raw, "alwaysLoad", "alwaysLoad")
	return converted, nil
}

func convertMCPServer(name string, server map[string]any) (map[string]any, error) {
	typ, _ := server["type"].(string)
	switch typ {
	case "", "local":
		return convertLocalMCPServer(name, server)
	case "remote":
		return convertRemoteMCPServer(server), nil
	default:
		return nil, fmt.Errorf("unsupported mcp server %q type %q", name, typ)
	}
}

func convertLocalMCPServer(name string, server map[string]any) (map[string]any, error) {
	command, args, err := commandParts(name, server["command"])
	if err != nil {
		return nil, err
	}
	converted := map[string]any{
		"type":    "stdio",
		"command": command,
	}
	if len(args) > 0 {
		converted["args"] = args
	}
	copyField(converted, server, "env", "env")
	copyField(converted, server, "environment", "env")
	copyField(converted, server, "timeout", "timeout")
	copyField(converted, server, "alwaysLoad", "alwaysLoad")
	return converted, nil
}

func convertRemoteMCPServer(server map[string]any) map[string]any {
	converted := map[string]any{"type": "http"}
	for _, key := range []string{"url", "headers", "oauth", "timeout", "alwaysLoad"} {
		copyField(converted, server, key, key)
	}
	return converted
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

func syntheticSettings() map[string]any {
	return map[string]any{}
}

func joinInstructions(instructions [][]byte) []byte {
	parts := make([][]byte, 0, len(instructions))
	for _, item := range instructions {
		if len(bytes.TrimSpace(item)) == 0 {
			continue
		}
		parts = append(parts, bytes.TrimRight(item, "\n"))
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
