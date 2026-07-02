// Package config generates Deep Agents Code synthetic configuration files Toby writes
// into dcode's real agent dir (~/.deepagents/toby): the MCP server list passed via
// --mcp-config and the Toby agent AGENTS.md written for default launches.
package config

import (
	"encoding/json"

	"petris.dev/toby/config/session"
	"petris.dev/toby/container/layout"
	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/tools/helpers"
)

// AgentDir is dcode's real Toby-agent directory.
const AgentDir = layout.Home + "/.deepagents/toby"

const (
	MCPConfigPath    = AgentDir + "/mcp.json"
	InstructionsPath = AgentDir + "/AGENTS.md"
)

func RegisterContextFiles(registrar contextfiles.Registrar, cfg sessionconfig.Config) error {
	mcp, err := marshalJSON(syntheticMCP(cfg.MCPServers))
	if err != nil {
		return err
	}
	return registrar.AddBytes(MCPConfigPath, mcp, 0o644)
}

func Instructions(cfg sessionconfig.Config) []byte {
	return helpers.JoinInstructions(cfg.Instructions.Contents)
}

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
