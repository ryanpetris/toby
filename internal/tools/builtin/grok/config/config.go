// Package config generates the synthetic Grok CLI configuration Toby writes to the
// tool's real home config path: managed_config.toml listing the MCP servers, and the
// combined instructions passed at launch via --rules. The input is the pre-resolved,
// sandbox-safe sessionconfig.Config; this package never sees the raw host config, the
// proxy, or any credential.
package config

import (
	"github.com/pelletier/go-toml/v2"

	"petris.dev/toby/config/session"
	"petris.dev/toby/container/layout"
	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/tools/helpers"
)

// ConfigPath is grok's real config file that the CLI reads.
const ConfigPath = layout.Home + "/.grok/managed_config.toml"

func RegisterContextFiles(registrar contextfiles.Registrar, cfg sessionconfig.Config) error {
	config, err := marshalConfig(syntheticMCPServers(cfg.MCPServers))
	if err != nil {
		return err
	}
	return registrar.AddBytes(ConfigPath, config, 0o644)
}

func Rules(instructions [][]byte) string {
	return helpers.JoinInstructionsString(instructions)
}

func syntheticMCPServers(servers []sessionconfig.MCPServer) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, server := range servers {
		out[server.Name] = map[string]any{
			"url":     server.URL,
			"enabled": true,
		}
	}
	return out
}

func marshalConfig(servers map[string]map[string]any) ([]byte, error) {
	return toml.Marshal(struct {
		MCPServers map[string]map[string]any `toml:"mcp_servers"`
	}{MCPServers: servers})
}
