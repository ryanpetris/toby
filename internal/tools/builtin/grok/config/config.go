// Package config renders Grok CLI's Toby-owned MCP configuration and
// instruction files from sandbox-safe session data.
package config

import (
	"fmt"

	"github.com/pelletier/go-toml/v2"

	"petris.dev/toby/internal/config/session"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/toolfiles"
	"petris.dev/toby/internal/tools/helpers"
)

const (
	// NativeConfigPath is Grok's generated native managed configuration path.
	NativeConfigPath = layout.Home + "/.grok/managed_config.toml"
	// NativeInstructionsPath is Grok's generated native instruction path.
	NativeInstructionsPath = layout.Home + "/.grok/AGENTS.md"
)

// NativeFiles renders Grok's generated native config and instruction files.
func NativeFiles(
	owner string,
	ownership toolfiles.Ownership,
	cfg sessionconfig.Config,
) ([]toolfiles.File, error) {
	config, err := renderConfig(cfg.MCPServers)
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

// Rules returns Grok's generated permission rules.
func Rules(instructions [][]byte) string {
	return helpers.JoinInstructionsString(instructions)
}

func renderConfig(servers []sessionconfig.MCPServer) ([]byte, error) {
	mcp, err := syntheticMCPServers(servers)
	if err != nil {
		return nil, err
	}
	return marshalConfig(mcp)
}

func syntheticMCPServers(servers []sessionconfig.MCPServer) (map[string]map[string]any, error) {
	out := map[string]map[string]any{}
	for _, server := range servers {
		if err := server.Validate(); err != nil {
			return nil, fmt.Errorf("render Grok MCP server %q: %w", server.Name, err)
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
				"render Grok MCP server %q: unsupported transport %q",
				server.Name,
				server.Transport,
			)
		}
		out[server.Name] = entry
	}
	return out, nil
}

func marshalConfig(servers map[string]map[string]any) ([]byte, error) {
	return toml.Marshal(struct {
		MCPServers map[string]map[string]any `toml:"mcp_servers"`
	}{MCPServers: servers})
}
