// Package config renders Codex's Toby-owned native configuration and
// highest-precedence per-run MCP command-line overrides.
package config

import (
	"fmt"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"petris.dev/toby/internal/config/session"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/toolfiles"
	"petris.dev/toby/internal/tools/helpers"
)

const (
	// NativeConfigPath is Codex's generated native configuration path.
	NativeConfigPath = layout.Home + "/.codex/config.toml"
	// NativeInstructionsPath is Codex's generated native instruction path.
	NativeInstructionsPath = layout.Home + "/.codex/AGENTS.md"
)

// MCPConfigArgs returns only the highest-precedence per-run MCP overrides.
// Native instructions remain file-based through AGENTS.md.
func MCPConfigArgs(cfg sessionconfig.Config) ([]string, error) {
	mcp, err := mcpConfig(cfg.MCPServers)
	if err != nil {
		return nil, err
	}
	if len(mcp) == 0 {
		return nil, nil
	}

	value, err := inlineMCPConfig(mcp)
	if err != nil {
		return nil, err
	}

	return []string{"-c", "mcp_servers=" + value}, nil
}

// NativeFiles renders Codex's complete generated config and instruction files.
func NativeFiles(
	owner string,
	ownership toolfiles.Ownership,
	cfg sessionconfig.Config,
) ([]toolfiles.File, error) {
	config, err := nativeConfig(cfg)
	if err != nil {
		return nil, err
	}

	return []toolfiles.File{
		{
			Owner:  owner,
			Target: NativeConfigPath,
			Data:   config,
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

func nativeConfig(cfg sessionconfig.Config) ([]byte, error) {
	mcp, err := mcpConfig(cfg.MCPServers)
	if err != nil {
		return nil, err
	}

	projects := make(
		map[string]map[string]string,
		len(cfg.Projects),
	)
	for _, project := range cfg.Projects {
		projects[project] = map[string]string{
			"trust_level": "trusted",
		}
	}

	return toml.Marshal(map[string]any{
		"mcp_servers": mcp,
		"projects":    projects,
	})
}

func mcpConfig(servers []sessionconfig.MCPServer) (map[string]map[string]any, error) {
	mcp := make(map[string]map[string]any, len(servers))
	for _, server := range servers {
		if err := server.Validate(); err != nil {
			return nil, fmt.Errorf("render Codex MCP server %q: %w", server.Name, err)
		}

		entry := map[string]any{"enabled": true}
		switch server.Transport {
		case sessionconfig.MCPTransportStdio:
			entry["command"] = server.Command
			entry["args"] = append([]string(nil), server.Args...)
		case sessionconfig.MCPTransportHTTP:
			entry["url"] = server.URL
		default:
			return nil, fmt.Errorf(
				"render Codex MCP server %q: unsupported transport %q",
				server.Name,
				server.Transport,
			)
		}
		mcp[server.Name] = entry
	}

	return mcp, nil
}

func inlineMCPConfig(mcp map[string]map[string]any) (string, error) {
	names := make([]string, 0, len(mcp))
	for name := range mcp {
		names = append(names, name)
	}
	sort.Strings(names)

	var rendered strings.Builder
	rendered.WriteByte('{')
	for nameIndex, name := range names {
		if nameIndex > 0 {
			rendered.WriteString(", ")
		}

		encodedName, err := tomlValue(name)
		if err != nil {
			return "", fmt.Errorf("encode Codex MCP server name %q: %w", name, err)
		}
		rendered.WriteString(encodedName)
		rendered.WriteString(" = {")

		entry := mcp[name]
		fields := make([]string, 0, len(entry))
		for field := range entry {
			fields = append(fields, field)
		}
		sort.Strings(fields)

		for fieldIndex, field := range fields {
			if fieldIndex > 0 {
				rendered.WriteString(", ")
			}

			encodedValue, err := tomlValue(entry[field])
			if err != nil {
				return "", fmt.Errorf(
					"encode Codex MCP server %q field %q: %w",
					name,
					field,
					err,
				)
			}
			rendered.WriteString(field)
			rendered.WriteString(" = ")
			rendered.WriteString(encodedValue)
		}
		rendered.WriteByte('}')
	}
	rendered.WriteByte('}')

	return rendered.String(), nil
}

func tomlValue(value any) (string, error) {
	data, err := toml.Marshal(map[string]any{"value": value})
	if err != nil {
		return "", err
	}

	line := strings.TrimSpace(string(data))
	_, encoded, ok := strings.Cut(line, " = ")
	if !ok {
		return "", fmt.Errorf("failed to encode TOML value: %q", line)
	}
	return encoded, nil
}
