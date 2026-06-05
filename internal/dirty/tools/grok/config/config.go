package config

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"petris.dev/toby/config/toby"
	"petris.dev/toby/context/files"
	"petris.dev/toby/control/httpproxy"
	"petris.dev/toby/internal/dirty/control/mcpproxy"
	"petris.dev/toby/internal/dirty/tools/toolconfig"
	"petris.dev/toby/internal/dirty/tools/toolconfig/proxyconfig"
)

const StaticConfigPath = "grok/config.toml"

func RegisterContextFiles(registrar contextfiles.Registrar, _ [][]byte, cfg *tobyconfig.Service, controlHost, tobyMCPURL string, proxy *httpproxy.Service, mcpProxy *mcpproxy.Service) error {
	config, err := syntheticConfig(cfg, controlHost, tobyMCPURL, proxy, mcpProxy)
	if err != nil {
		return err
	}
	return registrar.AddBytes(StaticConfigPath, config, 0o644)
}

func ConfigPath(contextDir string) string {
	return filepath.Join(contextDir, filepath.FromSlash(StaticConfigPath))
}

func Rules(instructions [][]byte) string {
	return toolconfig.JoinInstructionsString(instructions)
}

func syntheticConfig(cfg *tobyconfig.Service, controlHost, tobyMCPURL string, proxy *httpproxy.Service, mcpProxy *mcpproxy.Service) ([]byte, error) {
	servers, err := syntheticMCPServers(cfg, controlHost, tobyMCPURL, proxy, mcpProxy)
	if err != nil {
		return nil, err
	}
	return marshalConfig(servers)
}

func syntheticMCPServers(cfg *tobyconfig.Service, controlHost, tobyMCPURL string, proxy *httpproxy.Service, mcpProxy *mcpproxy.Service) (map[string]map[string]any, error) {
	servers := map[string]map[string]any{}
	if cfg != nil {
		for name, configured := range cfg.MCPServers() {
			if !configured.Enabled() {
				continue
			}
			if configured.HTTPProxyable() {
				converted, err := syntheticProxyMCP(controlHost, proxy, mcpProxy, name, configured)
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

func syntheticProxyMCP(controlHost string, proxy *httpproxy.Service, mcpProxy *mcpproxy.Service, name string, server tobyconfig.MCPServer) (map[string]any, error) {
	proxyURL, err := proxyconfig.MCPURL(controlHost, proxy, mcpProxy, name, server)
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
	command, args, err := toolconfig.CommandParts(name, server["command"])
	if err != nil {
		return nil, err
	}
	converted := map[string]any{"command": command}
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
	converted := map[string]any{"url": url}
	if typ != "" {
		converted["type"] = typ
	}
	copyCommonFields(converted, server)
	toolconfig.CopyField(converted, server, "headers", "headers")
	return converted, nil
}

func copyCommonFields(dst, src map[string]any) {
	for _, key := range []string{"enabled", "startup_timeout_sec", "tool_timeout_sec", "tool_timeouts"} {
		toolconfig.CopyField(dst, src, key, key)
	}
	toolconfig.CopyField(dst, src, "timeout", "startup_timeout_sec")
}

func marshalConfig(servers map[string]map[string]any) ([]byte, error) {
	return toml.Marshal(struct {
		MCPServers map[string]map[string]any `toml:"mcp_servers"`
	}{MCPServers: servers})
}
