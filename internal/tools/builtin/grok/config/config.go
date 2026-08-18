// Package config renders Grok CLI's Toby-owned plugin MCP configuration,
// config.toml plugin enablement patch, and instruction files from sandbox-safe
// session data.
package config

import (
	"encoding/json"
	"fmt"

	sessionconfig "petris.dev/toby/internal/config/session"
	"petris.dev/toby/internal/configpatch"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/toolfiles"
	"petris.dev/toby/internal/tools/helpers"
)

const (
	// PluginName is the Grok plugin id Toby generates under ~/.grok/plugins.
	PluginName = "toby-session"
	// NativeConfigPath is Grok's native configuration path.
	NativeConfigPath = layout.Home + "/.grok/config.toml"
	// NativePluginManifestPath is the generated Grok plugin manifest.
	NativePluginManifestPath = layout.Home + "/.grok/plugins/" + PluginName + "/plugin.json"
	// NativePluginMCPPath is the generated Grok plugin MCP configuration.
	NativePluginMCPPath = layout.Home + "/.grok/plugins/" + PluginName + "/.mcp.json"
	// NativeInstructionsPath is Grok's generated native instruction path.
	NativeInstructionsPath = layout.Home + "/.grok/AGENTS.md"
)

// NativeFiles renders Grok's generated plugin, config.toml enablement patch,
// and instruction files. Session MCP servers live only in the plugin so a
// later launch that omits a server replaces the previous set.
func NativeFiles(
	owner string,
	ownership toolfiles.Ownership,
	cfg sessionconfig.Config,
) ([]toolfiles.File, error) {
	manifest, err := renderPluginManifest()
	if err != nil {
		return nil, err
	}
	mcp, err := renderPluginMCP(cfg.MCPServers)
	if err != nil {
		return nil, err
	}

	return []toolfiles.File{
		{
			Owner:  owner,
			Target: NativePluginManifestPath,
			Data:   manifest,
			Mode:   0o644,
			UID:    ownership.UID,
			GID:    ownership.GID,
		},
		{
			Owner:  owner,
			Target: NativePluginMCPPath,
			Data:   mcp,
			Mode:   0o600,
			UID:    ownership.UID,
			GID:    ownership.GID,
		},
		{
			Owner:  owner,
			Target: NativeConfigPath,
			Patch: configpatch.Patch{
				Ensure: []configpatch.Value{{
					Path:  "/plugins/enabled",
					Value: PluginName,
				}},
			},
			Mode: 0o600,
			UID:  ownership.UID,
			GID:  ownership.GID,
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

// Rules returns Grok's generated permission rules.
func Rules(instructions [][]byte) string {
	return helpers.JoinInstructionsString(instructions)
}

func renderPluginManifest() ([]byte, error) {
	return marshalJSON(map[string]any{
		"name":        PluginName,
		"description": "Toby session MCP connectors",
	})
}

func renderPluginMCP(servers []sessionconfig.MCPServer) ([]byte, error) {
	mcp, err := syntheticMCP(servers)
	if err != nil {
		return nil, err
	}
	return marshalJSON(map[string]any{"mcpServers": mcp})
}

func syntheticMCP(servers []sessionconfig.MCPServer) (map[string]any, error) {
	out := map[string]any{}
	for _, server := range servers {
		if err := server.Validate(); err != nil {
			return nil, fmt.Errorf("render Grok MCP server %q: %w", server.Name, err)
		}

		switch server.Transport {
		case sessionconfig.MCPTransportStdio:
			out[server.Name] = map[string]any{
				"command": server.Command,
				"args":    append([]string(nil), server.Args...),
				"enabled": true,
			}
		case sessionconfig.MCPTransportHTTP:
			out[server.Name] = map[string]any{
				"url":     server.URL,
				"enabled": true,
			}
		default:
			return nil, fmt.Errorf(
				"render Grok MCP server %q: unsupported transport %q",
				server.Name,
				server.Transport,
			)
		}
	}
	return out, nil
}

func marshalJSON(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
