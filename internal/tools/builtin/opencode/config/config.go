// Package config generates the synthetic opencode.json that Toby writes to opencode's
// real config dir (~/.config/opencode): the MCP servers, the LLM providers (pointed at
// their proxied base URLs with models already resolved), the instruction file paths,
// and the permission paths. The input is the pre-resolved, sandbox-safe
// sessionconfig.Config; this package never sees the raw host config, the proxy, the
// provider registry, or any credential.
package config

import (
	"encoding/json"
	"strings"

	"petris.dev/toby/config/session"
	"petris.dev/toby/container/layout"
	contextfiles "petris.dev/toby/context/files"
)

// ConfigDir is opencode's real config directory (its XDG default).
const ConfigDir = layout.Home + "/.config/opencode"

const (
	StaticGitignorePath = ConfigDir + "/.gitignore"
	StaticConfigPath    = ConfigDir + "/opencode.json"
)

const (
	providerTypeAnthropic = "anthropic"
	providerTypeOpenAI    = "openai"
)

var opencodeGitignore = []byte("*\n")

// RegisterContextFiles renders opencode.json (and its .gitignore) from the
// resolved session config.
func RegisterContextFiles(registrar contextfiles.Registrar, cfg sessionconfig.Config) error {
	data, err := render(cfg)
	if err != nil {
		return err
	}

	if err := registrar.AddBytes(StaticGitignorePath, opencodeGitignore, 0o644); err != nil {
		return err
	}
	return registrar.AddBytes(StaticConfigPath, data, 0o644)
}

func render(cfg sessionconfig.Config) ([]byte, error) {
	config := map[string]any{"$schema": "https://opencode.ai/config.json"}
	if mcp := syntheticMCP(cfg.MCPServers); len(mcp) > 0 {
		config["mcp"] = mcp
	}
	if providers := syntheticProviders(cfg.Providers); len(providers) > 0 {
		config["provider"] = providers
	}
	addInstructions(config, cfg.Instructions.Paths)
	addPermissionPaths(config, cfg.Permissions)
	return marshalConfig(config)
}

func syntheticMCP(servers []sessionconfig.MCPServer) map[string]any {
	out := map[string]any{}
	for _, server := range servers {
		out[server.Name] = map[string]any{
			"type":    "remote",
			"url":     server.URL,
			"enabled": true,
		}
	}
	return out
}

func syntheticProviders(providers []sessionconfig.Provider) map[string]any {
	out := map[string]any{}
	for _, provider := range providers {
		entry := map[string]any{
			"options": map[string]any{"baseURL": provider.URL},
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
