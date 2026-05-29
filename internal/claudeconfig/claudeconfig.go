// Package claudeconfig generates the synthetic Claude Code configuration that
// Toby writes into the sandbox runtime context directory. Unlike OpenCode, Claude Code
// writes runtime state (credentials, history, transcripts) into its config
// directory, so Toby cannot redirect CLAUDE_CONFIG_DIR at a read-only mount.
// Instead the generated files here are passed to Claude via launch flags
// (--mcp-config, --settings, --append-system-prompt-file), which
// achieves the same injection OpenCode gets from its merged opencode.json.
package claudeconfig

import (
	"bytes"
	"encoding/json"

	"petris.dev/toby/internal/staticfiles"
)

const (
	// StaticMcpPath holds the toby MCP server definition (--mcp-config).
	StaticMcpPath = "claude/mcp.json"
	// StaticSettingsPath holds permission settings (--settings).
	StaticSettingsPath = "claude/settings.json"
	// StaticInstructionsPath holds the combined instruction text
	// (--append-system-prompt-file).
	StaticInstructionsPath = "claude/instructions.md"
)

// RegisterStaticFiles renders the Claude Code synthetic configuration files.
// instructions is the content of Toby's instruction files; they are concatenated
// into a single file so the launcher can pass exactly one
// --append-system-prompt-file.
func RegisterStaticFiles(registrar staticfiles.Registrar, projectRoot string, instructions [][]byte) error {
	mcp, err := marshalJSON(syntheticMCP())
	if err != nil {
		return err
	}
	settings, err := marshalJSON(syntheticSettings(projectRoot))
	if err != nil {
		return err
	}
	if err := registrar.AddBytes(StaticMcpPath, mcp, 0o400); err != nil {
		return err
	}
	if err := registrar.AddBytes(StaticSettingsPath, settings, 0o400); err != nil {
		return err
	}
	if err := registrar.AddBytes(StaticInstructionsPath, joinInstructions(instructions), 0o400); err != nil {
		return err
	}
	return nil
}

func syntheticMCP() map[string]any {
	return map[string]any{
		"mcpServers": map[string]any{
			"toby": map[string]any{
				"type":    "stdio",
				"command": "toby-sandbox",
				"args":    []any{"mcp"},
			},
		},
	}
}

func syntheticSettings(projectRoot string) map[string]any {
	return map[string]any{
		"permissions": map[string]any{
			"additionalDirectories": allowedDirectories(projectRoot),
		},
	}
}

// allowedDirectories mirrors opencodeconfig.allowedExternalDirectoryPatterns, but
// Claude's permissions.additionalDirectories takes directory paths rather than
// glob patterns, so the "/**" variants are omitted.
func allowedDirectories(projectRoot string) []any {
	dirs := []any{"/tmp"}
	if projectRoot != "" {
		dirs = append(dirs, projectRoot)
	}
	return dirs
}

func joinInstructions(instructions [][]byte) []byte {
	parts := make([][]byte, 0, len(instructions))
	for _, item := range instructions {
		if len(bytes.TrimSpace(item)) == 0 {
			continue
		}
		parts = append(parts, bytes.TrimRight(item, "\n"))
	}
	return append(bytes.Join(parts, []byte("\n\n")), '\n')
}

func marshalJSON(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
