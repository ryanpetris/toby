// Package config renders Cursor CLI's Toby-owned MCP configuration and
// instruction files from sandbox-safe session data.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"

	sessionconfig "petris.dev/toby/internal/config/session"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/toolfiles"
	"petris.dev/toby/internal/tools/helpers"
)

const (
	// NativeMCPPath is Cursor's generated native MCP configuration path.
	NativeMCPPath = layout.Home + "/.cursor/mcp.json"
	// NativeInstructionsPath is Cursor's generated always-applied rule.
	NativeInstructionsPath = layout.Home + "/.cursor/rules/toby.mdc"
)

// NativeFiles renders Cursor's generated native MCP and instruction files.
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
			Data:   renderRules(cfg.Instructions.Contents),
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
			return nil, fmt.Errorf("render Cursor MCP server %q: %w", server.Name, err)
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
				"render Cursor MCP server %q: unsupported transport %q",
				server.Name,
				server.Transport,
			)
		}
	}
	return map[string]any{"mcpServers": out}, nil
}

func renderRules(instructions [][]byte) []byte {
	var buf bytes.Buffer
	buf.WriteString("---\n")
	buf.WriteString("alwaysApply: true\n")
	buf.WriteString("---\n")
	if body := helpers.JoinInstructions(instructions); len(body) > 0 {
		buf.WriteByte('\n')
		buf.Write(body)
	}
	return buf.Bytes()
}

func marshalJSON(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
