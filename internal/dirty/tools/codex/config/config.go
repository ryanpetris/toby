package config

import (
	"fmt"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"petris.dev/toby/config/file"
	"petris.dev/toby/config/toby"
	"petris.dev/toby/control/httpproxy"
	"petris.dev/toby/internal/dirty/control/mcpproxy"
	"petris.dev/toby/internal/dirty/tools/proxyconfig"
	"petris.dev/toby/tools/toolconfig"
)

const TobyServerName = "toby"

// ConfigArgs returns Codex CLI config overrides for Toby's per-session
// synthetic context. Codex has no flag for an arbitrary config file, so use
// -c overrides and avoid writing profile files into CODEX_HOME.
func ConfigArgs(instructions [][]byte, cfg *tobyconfig.Service, controlHost, tobyMCPURL string, proxy *httpproxy.Service, mcpProxy *mcpproxy.Service) ([]string, error) {
	overrides := []string{}
	if strings.TrimSpace(tobyMCPURL) == "" {
		return nil, fmt.Errorf("toby MCP proxy URL is required")
	}
	items := []configItem{
		{key: "mcp_servers." + TobyServerName + ".url", value: strings.TrimSpace(tobyMCPURL)},
		{key: "mcp_servers." + TobyServerName + ".enabled", value: true},
	}
	configured, err := configuredMCPItems(cfg, controlHost, proxy, mcpProxy)
	if err != nil {
		return nil, err
	}
	items = append(items, configured...)
	for _, item := range items {
		override, err := configOverride(item.key, item.value)
		if err != nil {
			return nil, err
		}
		overrides = append(overrides, override)
	}
	if joined := toolconfig.JoinInstructionsString(instructions); joined != "" {
		override, err := configOverride("developer_instructions", joined)
		if err != nil {
			return nil, err
		}
		overrides = append(overrides, override)
	}
	args := make([]string, 0, len(overrides)*2)
	for _, override := range overrides {
		args = append(args, "-c", override)
	}
	return args, nil
}

type configItem struct {
	key   string
	value any
}

func configuredMCPItems(cfg *tobyconfig.Service, controlHost string, proxy *httpproxy.Service, mcpProxy *mcpproxy.Service) ([]configItem, error) {
	if cfg == nil {
		return nil, nil
	}
	servers := cfg.MCPServers()
	names := make([]string, 0, len(servers))
	for name, server := range servers {
		if name == TobyServerName || !server.Enabled() {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	items := []configItem{}
	for _, name := range names {
		server := servers[name]
		serverItems, err := mcpServerItems(name, server, controlHost, proxy, mcpProxy)
		if err != nil {
			return nil, err
		}
		items = append(items, serverItems...)
	}
	return items, nil
}

func mcpServerItems(name string, server tobyconfig.MCPServer, controlHost string, proxy *httpproxy.Service, mcpProxy *mcpproxy.Service) ([]configItem, error) {
	if server.HTTPProxyable() {
		url, err := proxyconfig.MCPURL(controlHost, proxy, mcpProxy, name, server)
		if err != nil {
			return nil, fmt.Errorf("mcp.%s: %w", name, err)
		}
		return []configItem{
			{key: "mcp_servers." + name + ".url", value: url},
			{key: "mcp_servers." + name + ".enabled", value: true},
		}, nil
	}
	return localMCPItems(name, server.Raw())
}

func localMCPItems(name string, server map[string]any) ([]configItem, error) {
	command, args, err := toolconfig.CommandParts(name, server["command"])
	if err != nil {
		return nil, err
	}
	items := []configItem{
		{key: "mcp_servers." + name + ".command", value: command},
		{key: "mcp_servers." + name + ".enabled", value: true},
	}
	if len(args) > 0 {
		items = append(items, configItem{key: "mcp_servers." + name + ".args", value: args})
	}
	if value, ok := server["env"]; ok {
		items = append(items, configItem{key: "mcp_servers." + name + ".env", value: configfile.Clone(value)})
	} else if value, ok := server["environment"]; ok {
		items = append(items, configItem{key: "mcp_servers." + name + ".env", value: configfile.Clone(value)})
	}
	for _, key := range []string{"cwd", "startup_timeout_sec", "tool_timeout_sec"} {
		if value, ok := server[key]; ok {
			items = append(items, configItem{key: "mcp_servers." + name + "." + key, value: configfile.Clone(value)})
		}
	}
	return items, nil
}

func configOverride(key string, value any) (string, error) {
	encoded, err := tomlValue(value)
	if err != nil {
		return "", err
	}
	return key + "=" + encoded, nil
}

func tomlValue(value any) (string, error) {
	data, err := toml.Marshal(map[string]any{"value": value})
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(data))
	_, encoded, ok := strings.Cut(line, " = ")
	if !ok {
		return "", fmt.Errorf("failed to encode TOML value: %q", line)
	}
	return encoded, nil
}
