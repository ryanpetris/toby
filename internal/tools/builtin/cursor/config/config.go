// Package config renders Cursor CLI's Toby-owned MCP configuration and
// instruction files from sandbox-safe session data.
package config

import (
	"bytes"

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
	mcp, err := helpers.StdioHTTPMCPServers(servers, "Cursor")
	if err != nil {
		return nil, err
	}
	return helpers.MarshalJSON(mcp)
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
