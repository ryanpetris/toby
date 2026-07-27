// Package config renders Deep Agents Code's Toby-owned MCP and Toby-agent
// instruction files from sandbox-safe session data.
package config

import (
	"encoding/json"
	"fmt"

	"petris.dev/toby/internal/config/session"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/toolfiles"
	"petris.dev/toby/internal/tools/helpers"
)

const (
	// NativeMCPPath is DCode's generated native MCP configuration path.
	NativeMCPPath = layout.Home + "/.deepagents/.mcp.json"
	// NativeInstructionsPath is the generated Toby-agent instruction path.
	NativeInstructionsPath = layout.Home + "/.deepagents/toby/AGENTS.md"
)

// NativeFiles renders DCode's generated native MCP and Toby-agent files.
func NativeFiles(
	owner string,
	ownership toolfiles.Ownership,
	cfg sessionconfig.Config,
) ([]toolfiles.File, error) {
	mcp, err := renderMCP(cfg.MCPServers)
	if err != nil {
		return nil, err
	}

	return []toolfiles.File{
		{
			Owner:  owner,
			Target: NativeMCPPath,
			Data:   mcp,
			Mode:   0o600,
			UID:    ownership.UID,
			GID:    ownership.GID,
		},
		{
			Owner:  owner,
			Target: NativeInstructionsPath,
			Data:   Instructions(cfg),
			Mode:   0o644,
			UID:    ownership.UID,
			GID:    ownership.GID,
		},
	}, nil
}

// Instructions returns the configured instruction files.
func Instructions(cfg sessionconfig.Config) []byte {
	return helpers.JoinInstructions(cfg.Instructions.Contents)
}

func renderMCP(servers []sessionconfig.MCPServer) ([]byte, error) {
	mcp, err := syntheticMCP(servers)
	if err != nil {
		return nil, err
	}
	return marshalJSON(mcp)
}

func syntheticMCP(servers []sessionconfig.MCPServer) (map[string]any, error) {
	out := map[string]any{}
	for _, server := range servers {
		if err := server.Validate(); err != nil {
			return nil, fmt.Errorf("render DCode MCP server %q: %w", server.Name, err)
		}

		switch server.Transport {
		case sessionconfig.MCPTransportStdio:
			out[server.Name] = map[string]any{
				"type":    "stdio",
				"command": server.Command,
				"args":    append([]string(nil), server.Args...),
			}
		case sessionconfig.MCPTransportHTTP:
			out[server.Name] = map[string]any{
				"type": "http",
				"url":  server.URL,
			}
		default:
			return nil, fmt.Errorf(
				"render DCode MCP server %q: unsupported transport %q",
				server.Name,
				server.Transport,
			)
		}
	}
	return map[string]any{"mcpServers": out}, nil
}

func marshalJSON(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
