package config

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"petris.dev/toby/internal/config/toby"
	"petris.dev/toby/internal/context/files"
	"petris.dev/toby/internal/control/httpproxy"
	"petris.dev/toby/internal/tools/toolconfig"
	"petris.dev/toby/internal/tools/toolconfig/proxyconfig"
)

const (
	StaticMCPPath          = "copilot/mcp-config.json"
	StaticInstructionsPath = "copilot/AGENTS.md"
)

func RegisterContextFiles(registrar contextfiles.Registrar, instructions [][]byte, cfg *tobyconfig.Service, controlHost, tobyMCPURL string, proxy *httpproxy.Service) error {
	mcpConfig, err := syntheticMCP(cfg, controlHost, tobyMCPURL, proxy)
	if err != nil {
		return err
	}
	mcp, err := marshalJSON(mcpConfig)
	if err != nil {
		return err
	}
	if err := registrar.AddBytes(StaticMCPPath, mcp, 0o644); err != nil {
		return err
	}
	return registrar.AddBytes(StaticInstructionsPath, toolconfig.JoinInstructions(instructions), 0o644)
}

func MCPConfigPath(contextDir string) string {
	return filepath.Join(contextDir, filepath.FromSlash(StaticMCPPath))
}

func InstructionsDir(contextDir string) string {
	return filepath.Join(contextDir, "copilot")
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
		"type":  "http",
		"url":   strings.TrimSpace(url),
		"tools": []any{"*"},
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
			return convertRemoteMCPServer(name, "http", server)
		}
		return nil, fmt.Errorf("mcp server %q command or url is required", name)
	case "local":
		return convertLocalMCPServer(name, server)
	case "remote":
		return convertRemoteMCPServer(name, "http", server)
	default:
		return nil, fmt.Errorf("unsupported Copilot mcp server %q type %q", name, typ)
	}
}

func convertLocalMCPServer(name string, server map[string]any) (map[string]any, error) {
	command, args, err := toolconfig.CommandParts(name, server["command"])
	if err != nil {
		return nil, err
	}
	converted := map[string]any{"type": "stdio", "command": command}
	if len(args) > 0 {
		converted["args"] = args
	}
	copyCommonFields(converted, server)
	toolconfig.CopyField(converted, server, "env", "env")
	toolconfig.CopyField(converted, server, "environment", "env")
	toolconfig.CopyField(converted, server, "cwd", "cwd")
	return converted, nil
}

func convertRemoteMCPServer(name, typ string, server map[string]any) (map[string]any, error) {
	url, ok := server["url"].(string)
	if !ok || url == "" {
		return nil, fmt.Errorf("mcp server %q url is required", name)
	}
	converted := map[string]any{"type": typ, "url": url}
	copyCommonFields(converted, server)
	toolconfig.CopyField(converted, server, "headers", "headers")
	return converted, nil
}

func copyCommonFields(dst, src map[string]any) {
	for _, key := range []string{"enabled", "tools"} {
		toolconfig.CopyField(dst, src, key, key)
	}
}

func marshalJSON(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
