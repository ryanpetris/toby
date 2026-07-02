// Package config generates the synthetic Claude Code configuration that Toby writes
// into Claude's real config dir (~/.config/claude, also CLAUDE_CONFIG_DIR). Claude
// writes runtime state (credentials, history, transcripts) there too. The generated
// files here are passed to Claude via launch flags (--mcp-config, --settings,
// --append-system-prompt-file), which achieves the same injection OpenCode gets from
// its merged opencode.json. The input is the pre-resolved, sandbox-safe
// sessionconfig.Config; this package never sees the raw host config, the proxy, or any
// credential.
package config

import (
	"encoding/json"
	"sort"
	"strings"

	"petris.dev/toby/config/session"
	"petris.dev/toby/container/layout"
	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/tools/helpers"
)

// ConfigDir is Claude's real config directory (matches CLAUDE_CONFIG_DIR).
const ConfigDir = layout.Home + "/.config/claude"

const (
	// StaticMcpPath holds the toby MCP server definition (--mcp-config).
	StaticMcpPath = ConfigDir + "/mcp.json"
	// StaticSettingsPath holds generated Claude settings (--settings).
	StaticSettingsPath = ConfigDir + "/settings.json"
	// StaticInstructionsPath holds the combined instruction text
	// (--append-system-prompt-file).
	StaticInstructionsPath = ConfigDir + "/instructions.md"
)

// RegisterContextFiles renders the Claude Code synthetic configuration files
// from the resolved session config. Instruction contents are concatenated into a
// single file so the launcher can pass exactly one --append-system-prompt-file.
func RegisterContextFiles(registrar contextfiles.Registrar, cfg sessionconfig.Config) error {
	mcp, err := marshalJSON(syntheticMCP(cfg.MCPServers))
	if err != nil {
		return err
	}

	settings, err := marshalJSON(syntheticSettings(cfg.Permissions))
	if err != nil {
		return err
	}

	if err := registrar.AddBytes(StaticMcpPath, mcp, 0o644); err != nil {
		return err
	}
	if err := registrar.AddBytes(StaticSettingsPath, settings, 0o644); err != nil {
		return err
	}
	return registrar.AddBytes(StaticInstructionsPath, helpers.JoinInstructionsOrNewline(cfg.Instructions.Contents), 0o644)
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
