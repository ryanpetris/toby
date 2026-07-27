// Package config renders Claude Code's Toby-owned MCP, settings, and
// instruction files from sandbox-safe session data.
package config

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"petris.dev/toby/internal/config/session"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/toolfiles"
	"petris.dev/toby/internal/tools/helpers"
)

const (
	// NativeMCPPath is Claude's dedicated generated MCP file.
	NativeMCPPath = layout.Home + "/.config/claude/toby/mcp.json"
	// NativeSettingsPath is Claude's dedicated generated settings file.
	NativeSettingsPath = layout.Home + "/.config/claude/toby/settings.json"
	// NativeInstructionsPath is Claude's generated native instruction file.
	NativeInstructionsPath = layout.Home + "/.config/claude/CLAUDE.md"
)

// NativeFiles renders Claude's generated files beneath its configured native
// config directory without touching authentication or history files.
func NativeFiles(
	owner string,
	ownership toolfiles.Ownership,
	cfg sessionconfig.Config,
) ([]toolfiles.File, error) {
	mcp, err := renderMCP(cfg.MCPServers)
	if err != nil {
		return nil, err
	}
	settings, err := marshalJSON(syntheticSettings(cfg.Permissions))
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
			Target: NativeSettingsPath,
			Data:   settings,
			Mode:   0o600,
			UID:    ownership.UID,
			GID:    ownership.GID,
		},
		{
			Owner:  owner,
			Target: NativeInstructionsPath,
			Data:   helpers.JoinInstructionsOrNewline(cfg.Instructions.Contents),
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
			return nil, fmt.Errorf("render Claude MCP server %q: %w", server.Name, err)
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
				"render Claude MCP server %q: unsupported transport %q",
				server.Name,
				server.Transport,
			)
		}
	}
	return map[string]any{"mcpServers": out}, nil
}

// syntheticSettings renders Claude's permission settings from Toby's shared
// permission paths. Claude's permissions.additionalDirectories takes directory
// paths rather than glob patterns, so glob entries are dropped and only the
// "allow" directories are listed.
func syntheticSettings(permissions map[string]string) map[string]any {
	dirs := allowedDirectories(permissions)
	if len(dirs) == 0 {
		return map[string]any{}
	}
	return map[string]any{
		"permissions": map[string]any{
			"additionalDirectories": dirs,
		},
	}
}

func allowedDirectories(permissions map[string]string) []any {
	dirs := make([]string, 0, len(permissions))
	for pattern, mode := range permissions {
		if mode != "allow" {
			continue
		}
		// Claude's additionalDirectories takes directory paths, not globs; the
		// path is otherwise listed verbatim.
		if strings.ContainsAny(pattern, "*?[") {
			continue
		}
		dirs = append(dirs, pattern)
	}
	sort.Strings(dirs)
	result := make([]any, len(dirs))
	for i, dir := range dirs {
		result[i] = dir
	}
	return result
}

func marshalJSON(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
