// Package config renders GitHub Copilot CLI's Toby-owned MCP and instruction
// files from sandbox-safe session data.
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
	// NativeMCPPath is Copilot's generated native MCP configuration path.
	NativeMCPPath = layout.Home + "/.copilot/mcp-config.json"
	// NativeInstructionsPath is Copilot's generated native instruction path.
	NativeInstructionsPath = layout.Home + "/.copilot/copilot-instructions.md"
)

// NativeFiles renders Copilot's complete generated native configuration.
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
			Data:   helpers.JoinInstructions(cfg.Instructions.Contents),
			Mode:   0o644,
			UID:    ownership.UID,
			GID:    ownership.GID,
		},
	}, nil
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
			return nil, fmt.Errorf("render Copilot MCP server %q: %w", server.Name, err)
		}

		switch server.Transport {
		case sessionconfig.MCPTransportStdio:
			out[server.Name] = map[string]any{
				"type":    "local",
				"command": server.Command,
				"args":    append([]string(nil), server.Args...),
				"tools":   []string{"*"},
			}
		case sessionconfig.MCPTransportHTTP:
			out[server.Name] = map[string]any{
				"type":  "http",
				"url":   server.URL,
				"tools": []string{"*"},
			}
		default:
			return nil, fmt.Errorf(
				"render Copilot MCP server %q: unsupported transport %q",
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
