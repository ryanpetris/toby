// Package config generates the synthetic Claude Code configuration that
// Toby writes into the sandbox runtime context directory. Unlike OpenCode, Claude Code
// writes runtime state (credentials, history, transcripts) into its config
// directory, so Toby leaves Claude's config directory on managed mount backing.
// The generated files here are passed to Claude via launch flags
// (--mcp-config, --settings, --append-system-prompt-file), which
// achieves the same injection OpenCode gets from its merged opencode.json.
package config

import (
	"encoding/json"
	"fmt"
	"strings"

	"petris.dev/toby/internal/config/toby"
	"petris.dev/toby/internal/context/files"
	"petris.dev/toby/internal/control/httpproxy"
	"petris.dev/toby/internal/tools/toolconfig"
	"petris.dev/toby/internal/tools/toolconfig/proxyconfig"
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
	if err := registrar.AddBytes(StaticMcpPath, mcp, 0o644); err != nil {
		return err
	}
	if err := registrar.AddBytes(StaticSettingsPath, settings, 0o644); err != nil {
		return err
	}
	if err := registrar.AddBytes(StaticInstructionsPath, toolconfig.JoinInstructionsOrNewline(instructions), 0o644); err != nil {
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
	toolconfig.CopyField(converted, raw, "enabled", "enabled")
	toolconfig.CopyField(converted, raw, "timeout", "timeout")
	toolconfig.CopyField(converted, raw, "alwaysLoad", "alwaysLoad")
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
	command, args, err := toolconfig.CommandParts(name, server["command"])
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
	toolconfig.CopyField(converted, server, "env", "env")
	toolconfig.CopyField(converted, server, "environment", "env")
	toolconfig.CopyField(converted, server, "timeout", "timeout")
	toolconfig.CopyField(converted, server, "alwaysLoad", "alwaysLoad")
	return converted, nil
}

func convertRemoteMCPServer(server map[string]any) map[string]any {
	converted := map[string]any{"type": "http"}
	for _, key := range []string{"url", "headers", "oauth", "timeout", "alwaysLoad"} {
		toolconfig.CopyField(converted, server, key, key)
	}
	return converted
}

func syntheticSettings() map[string]any {
	return map[string]any{}
}

func marshalJSON(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
