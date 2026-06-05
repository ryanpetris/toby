// Package config builds the Codex CLI config overrides for Toby's per-session
// synthetic context. Codex has no flag for an arbitrary config file, so the MCP
// servers and instructions are injected as -c overrides instead of a written
// file. The input is the pre-resolved, sandbox-safe sessionconfig.Config; this
// package never sees the raw host config, the proxy, or any credential.
package config

import (
	"fmt"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"petris.dev/toby/config/session"
	"petris.dev/toby/tools/helpers"
)

// ConfigArgs returns Codex CLI config overrides (-c key=value) for the resolved
// session config: each MCP server as an mcp_servers.<name> entry pointed at its
// proxied URL, plus the combined developer instructions.
func ConfigArgs(cfg sessionconfig.Config) ([]string, error) {
	items := make([]configItem, 0, len(cfg.MCPServers)*2)
	for _, server := range cfg.MCPServers {
		items = append(items,
			configItem{key: "mcp_servers." + server.Name + ".url", value: server.URL},
			configItem{key: "mcp_servers." + server.Name + ".enabled", value: true},
		)
	}

	overrides := make([]string, 0, len(items)+1)
	for _, item := range items {
		override, err := configOverride(item.key, item.value)
		if err != nil {
			return nil, err
		}
		overrides = append(overrides, override)
	}

	if joined := helpers.JoinInstructionsString(cfg.Instructions.Contents); joined != "" {
		override, err := configOverride("developer_instructions", joined)
		if err != nil {
			return nil, err
		}
		overrides = append(overrides, override)
	}

	args := make([]string, 0, len(overrides)*2)
	for _, override := range overrides {
		args = append(args, "-c", override)
	}
	return args, nil
}

type configItem struct {
	key   string
	value any
}

func configOverride(key string, value any) (string, error) {
	encoded, err := tomlValue(value)
	if err != nil {
		return "", err
	}
	return key + "=" + encoded, nil
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
