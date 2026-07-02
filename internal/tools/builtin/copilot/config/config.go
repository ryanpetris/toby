// Package config generates the synthetic GitHub Copilot CLI configuration Toby writes
// into Copilot's real config dir (~/.config/copilot): the MCP server list (passed via
// --additional-mcp-config) and the combined AGENTS.md instructions (pointed at via
// COPILOT_CUSTOM_INSTRUCTIONS_DIRS). The input is the pre-resolved, sandbox-safe
// sessionconfig.Config; this package never sees the raw host config, the proxy, or any
// credential.
package config

import (
	"encoding/json"

	"petris.dev/toby/config/session"
	"petris.dev/toby/container/layout"
	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/tools/helpers"
)

// ConfigDir is Copilot's real config directory.
const ConfigDir = layout.Home + "/.config/copilot"

const (
	StaticMCPPath          = ConfigDir + "/mcp-config.json"
	StaticInstructionsPath = ConfigDir + "/AGENTS.md"
)

func RegisterContextFiles(registrar contextfiles.Registrar, cfg sessionconfig.Config) error {
	mcp, err := marshalJSON(syntheticMCP(cfg.MCPServers))
	if err != nil {
		return err
	}

	if err := registrar.AddBytes(StaticMCPPath, mcp, 0o644); err != nil {
		return err
	}
	return registrar.AddBytes(StaticInstructionsPath, helpers.JoinInstructions(cfg.Instructions.Contents), 0o644)
}

// MCPConfigPath is the generated MCP config file (--additional-mcp-config).
func MCPConfigPath() string { return StaticMCPPath }

// InstructionsDir is the dir holding AGENTS.md (COPILOT_CUSTOM_INSTRUCTIONS_DIRS).
func InstructionsDir() string { return ConfigDir }

func syntheticMCP(servers []sessionconfig.MCPServer) map[string]any {
	out := map[string]any{}
	for _, server := range servers {
		out[server.Name] = map[string]any{
			"type": "http",
			"url":  server.URL,
		}
	}
	return map[string]any{"mcpServers": out}
}

func marshalJSON(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
