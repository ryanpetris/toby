// Package config generates Deep Agents Code synthetic configuration files Toby
// writes into the sandbox context directory: the MCP server list passed via
// --mcp-config and the optional Toby agent AGENTS.md linked only for default
// launches.
package config

import (
	"encoding/json"
	"path/filepath"

	"petris.dev/toby/config/session"
	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/tools/helpers"
)

const (
	StaticMCPPath          = "dcode/mcp.json"
	StaticInstructionsPath = "dcode/AGENTS.md"
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

func MCPConfigPath(contextDir string) string {
	return filepath.Join(contextDir, filepath.FromSlash(StaticMCPPath))
}

func InstructionsPath(contextDir string) string {
	return filepath.Join(contextDir, filepath.FromSlash(StaticInstructionsPath))
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
