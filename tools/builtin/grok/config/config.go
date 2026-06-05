// Package config generates the synthetic Grok CLI configuration Toby writes into
// the sandbox context directory: a config.toml listing the MCP servers, and the
// combined instructions passed at launch via --rules. The input is the
// pre-resolved, sandbox-safe sessionconfig.Config; this package never sees the
// raw host config, the proxy, or any credential.
package config

import (
	"path/filepath"

	"github.com/pelletier/go-toml/v2"

	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/sessionconfig"
	"petris.dev/toby/tools/helpers"
)

const StaticConfigPath = "grok/config.toml"

func RegisterContextFiles(registrar contextfiles.Registrar, cfg sessionconfig.Config) error {
	config, err := marshalConfig(syntheticMCPServers(cfg.MCPServers))
	if err != nil {
		return err
	}
	return registrar.AddBytes(StaticConfigPath, config, 0o644)
}

func ConfigPath(contextDir string) string {
	return filepath.Join(contextDir, filepath.FromSlash(StaticConfigPath))
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
