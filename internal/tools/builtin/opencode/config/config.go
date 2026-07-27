// Package config renders OpenCode's Toby-owned configuration and instruction
// files from sandbox-safe session data.
package config

import (
	"encoding/json"
	"fmt"
	"strings"

	"petris.dev/toby/internal/config/session"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/toolfiles"
	"petris.dev/toby/internal/tools/helpers"
)

const (
	// NativeConfigPath is OpenCode's generated native configuration path.
	NativeConfigPath = layout.Home + "/.config/opencode/opencode.json"
	// NativePriorityConfigPath is the distinct custom-directory copy loaded
	// after project configuration.
	NativePriorityConfigPath = layout.Home +
		"/.config/opencode/toby/opencode.json"
	// NativeInstructionsPath is OpenCode's generated native instruction path.
	NativeInstructionsPath = layout.Home + "/.config/opencode/AGENTS.md"
)

const (
	providerTypeAnthropic = "anthropic"
	providerTypeOpenAI    = "openai"
)

// NativeFiles renders the regular files OpenCode consumes from its native
// configuration directory.
func NativeFiles(
	owner string,
	ownership toolfiles.Ownership,
	cfg sessionconfig.Config,
) ([]toolfiles.File, error) {
	data, err := NativeConfig(cfg)
	if err != nil {
		return nil, err
	}

	return []toolfiles.File{
		{
			Owner:  owner,
			Target: NativeConfigPath,
			Data:   data,
			Mode:   0o600,
			UID:    ownership.UID,
			GID:    ownership.GID,
		},
		{
			Owner:  owner,
			Target: NativePriorityConfigPath,
			Data:   append([]byte(nil), data...),
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

// NativeConfig renders the content shared by OpenCode's generated native
// config copies.
func NativeConfig(cfg sessionconfig.Config) ([]byte, error) {
	return render(cfg, []string{NativeInstructionsPath})
}

func render(cfg sessionconfig.Config, instructionPaths []string) ([]byte, error) {
	config := map[string]any{"$schema": "https://opencode.ai/config.json"}
	mcp, err := syntheticMCP(cfg.MCPServers)
	if err != nil {
		return nil, err
	}
	if len(mcp) > 0 {
		config["mcp"] = mcp
	}
	if providers := syntheticProviders(cfg.Models); len(providers) > 0 {
		config["provider"] = providers
	}
	addInstructions(config, instructionPaths)
	addPermissionPaths(config, cfg.Permissions)
	return marshalConfig(config)
}

func syntheticMCP(servers []sessionconfig.MCPServer) (map[string]any, error) {
	out := map[string]any{}
	for _, server := range servers {
		if err := server.Validate(); err != nil {
			return nil, fmt.Errorf("render OpenCode MCP server %q: %w", server.Name, err)
		}

		switch server.Transport {
		case sessionconfig.MCPTransportStdio:
			out[server.Name] = map[string]any{
				"type":    "local",
				"command": append([]string{server.Command}, server.Args...),
				"enabled": true,
			}
		case sessionconfig.MCPTransportHTTP:
			out[server.Name] = map[string]any{
				"type":    "remote",
				"url":     server.URL,
				"enabled": true,
			}
		default:
			return nil, fmt.Errorf(
				"render OpenCode MCP server %q: unsupported transport %q",
				server.Name,
				server.Transport,
			)
		}
	}
	return out, nil
}

func syntheticProviders(providers []sessionconfig.ModelsEndpoint) map[string]any {
	out := map[string]any{}
	for _, provider := range providers {
		options := map[string]any{"baseURL": provider.URL}
		if provider.Credential != "" {
			options["apiKey"] = provider.Credential
		}
		entry := map[string]any{
			"options": options,
		}
		if provider.Type == providerTypeAnthropic {
			entry["npm"] = "@ai-sdk/anthropic"
		} else {
			entry["npm"] = "@ai-sdk/openai-compatible"
		}
		if provider.Name != "" {
			entry["name"] = provider.Name
		}
		if len(provider.Models) > 0 {
			entry["models"] = provider.Models
		}
		out[provider.ID] = entry
	}
	return out
}

func addPermissionPaths(config map[string]any, paths map[string]string) {
	if len(paths) == 0 {
		return
	}
	permission := objectAt(config, "permission")
	external, ok := permission["external_directory"].(map[string]any)
	if !ok {
		external = map[string]any{}
		permission["external_directory"] = external
	}
	for pattern, mode := range paths {
		for _, expanded := range expandDirectoryPattern(pattern) {
			external[expanded] = mode
		}
	}
}

// expandDirectoryPattern turns a Toby permission path into the patterns opencode's
// external_directory expects. Permission paths are always directories, so each is
// emitted verbatim plus a recursive glob covering its subtree (e.g. "/foobar" ->
// "/foobar", "/foobar/**"; "/foobar/" -> "/foobar/", "/foobar/**"). The trailing
// slash is never stripped.
func expandDirectoryPattern(pattern string) []string {
	if strings.HasSuffix(pattern, "/") {
		return []string{pattern, pattern + "**"}
	}
	return []string{pattern, pattern + "/**"}
}

func addInstructions(config map[string]any, paths []string) {
	if len(paths) == 0 {
		return
	}
	instructions, ok := config["instructions"].([]any)
	if !ok {
		instructions = []any{}
	}
	seen := map[string]bool{}
	for _, item := range instructions {
		if path, ok := item.(string); ok {
			seen[path] = true
		}
	}
	for _, path := range paths {
		if path == "" || seen[path] {
			continue
		}
		instructions = append(instructions, path)
		seen[path] = true
	}
	config["instructions"] = instructions
}

func objectAt(config map[string]any, key string) map[string]any {
	if value, ok := config[key].(map[string]any); ok {
		return value
	}
	value := map[string]any{}
	config[key] = value
	return value
}

func marshalConfig(config map[string]any) ([]byte, error) {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
