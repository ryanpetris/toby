package codexconfig

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"petris.dev/toby/internal/control"
)

const TobyServerName = "toby"

// ConfigArgs returns Codex CLI config overrides for Toby's per-session
// synthetic context. Codex has no flag for an arbitrary config file, so use
// -c overrides and avoid writing profile files into CODEX_HOME.
func ConfigArgs(instructions [][]byte) ([]string, error) {
	overrides := []string{}
	for _, item := range []struct {
		key   string
		value any
	}{
		{key: "mcp_servers." + TobyServerName + ".command", value: "toby"},
		{key: "mcp_servers." + TobyServerName + ".args", value: []string{"sandbox", "mcp"}},
		{key: "mcp_servers." + TobyServerName + ".enabled", value: true},
		{key: "mcp_servers." + TobyServerName + ".env_vars", value: []string{control.EnvControlURL, control.EnvControlToken}},
	} {
		override, err := configOverride(item.key, item.value)
		if err != nil {
			return nil, err
		}
		overrides = append(overrides, override)
	}
	if joined := joinInstructions(instructions); joined != "" {
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

func configOverride(key string, value any) (string, error) {
	encoded, err := tomlValue(value)
	if err != nil {
		return "", err
	}
	return key + "=" + encoded, nil
}

func joinInstructions(instructions [][]byte) string {
	parts := make([][]byte, 0, len(instructions))
	for _, item := range instructions {
		if len(bytes.TrimSpace(item)) == 0 {
			continue
		}
		parts = append(parts, bytes.TrimRight(item, "\n"))
	}
	if len(parts) == 0 {
		return ""
	}
	return string(append(bytes.Join(parts, []byte("\n\n")), '\n'))
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
