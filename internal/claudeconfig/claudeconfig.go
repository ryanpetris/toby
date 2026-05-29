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
	"fmt"

	"petris.dev/toby/internal/configfile"
	"petris.dev/toby/internal/contextfiles"
	"petris.dev/toby/internal/tobyconfig"
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

// RegisterContextFiles renders the Claude Code synthetic configuration files.
// instructions is the content of Toby's instruction files; they are concatenated
// into a single file so the launcher can pass exactly one
// --append-system-prompt-file.
func RegisterContextFiles(registrar contextfiles.Registrar, projectRoot string, instructions [][]byte, cfg *tobyconfig.Service) error {
	mcpConfig, err := syntheticMCP(cfg)
	if err != nil {
		return err
	}
	mcp, err := marshalJSON(mcpConfig)
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

func syntheticMCP(cfg *tobyconfig.Service) (map[string]any, error) {
	servers := map[string]any{}
	if cfg != nil {
		for name, configured := range cfg.MCPServers() {
			if !configured.Enabled() {
				continue
			}
			converted, err := convertMCPServer(name, configured.Raw())
			if err != nil {
				return nil, err
			}
			servers[name] = converted
		}
	}
	servers["toby"] = syntheticTobyMCP()
	return map[string]any{"mcpServers": servers}, nil
}

func syntheticTobyMCP() map[string]any {
	return map[string]any{
		"type":    "stdio",
		"command": "toby-sandbox",
		"args":    []any{"mcp"},
	}
}

func convertMCPServer(name string, server map[string]any) (map[string]any, error) {
	typ, _ := server["type"].(string)
	switch typ {
	case "", "local":
		return convertLocalMCPServer(name, server)
	case "remote":
		return convertRemoteMCPServer(server), nil
	case "stdio", "http", "streamable-http", "sse", "ws":
		return configfile.CloneMap(server), nil
	default:
		return nil, fmt.Errorf("unsupported mcp server %q type %q", name, typ)
	}
}

func convertLocalMCPServer(name string, server map[string]any) (map[string]any, error) {
	command, args, err := commandParts(name, server["command"])
	if err != nil {
		return nil, err
	}
	converted := map[string]any{
		"type":    "stdio",
		"command": command,
	}
	if len(args) > 0 {
		converted["args"] = args
	}
	copyField(converted, server, "env", "env")
	copyField(converted, server, "environment", "env")
	copyField(converted, server, "timeout", "timeout")
	copyField(converted, server, "alwaysLoad", "alwaysLoad")
	return converted, nil
}

func convertRemoteMCPServer(server map[string]any) map[string]any {
	converted := map[string]any{"type": "http"}
	for _, key := range []string{"url", "headers", "oauth", "timeout", "alwaysLoad"} {
		copyField(converted, server, key, key)
	}
	return converted
}

func commandParts(name string, raw any) (string, []any, error) {
	switch command := raw.(type) {
	case string:
		if command == "" {
			return "", nil, fmt.Errorf("mcp server %q command is empty", name)
		}
		return command, nil, nil
	case []any:
		if len(command) == 0 {
			return "", nil, fmt.Errorf("mcp server %q command is empty", name)
		}
		first, ok := command[0].(string)
		if !ok || first == "" {
			return "", nil, fmt.Errorf("mcp server %q command must start with a string", name)
		}
		args := make([]any, 0, len(command)-1)
		for _, item := range command[1:] {
			arg, ok := item.(string)
			if !ok {
				return "", nil, fmt.Errorf("mcp server %q command arguments must be strings", name)
			}
			args = append(args, arg)
		}
		return first, args, nil
	default:
		return "", nil, fmt.Errorf("mcp server %q command is required", name)
	}
}

func copyField(dst, src map[string]any, from, to string) {
	if value, ok := src[from]; ok {
		dst[to] = configfile.Clone(value)
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
