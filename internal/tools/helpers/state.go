package helpers

import (
	"fmt"
	"path/filepath"
	"strings"

	"petris.dev/toby/internal/tools/tool"
)

func ParseToolState(value string) (tool.ToolState, error) {
	switch state := tool.ToolState(strings.TrimSpace(value)); state {
	case tool.ToolStatePrivate, tool.ToolStateHost:
		return state, nil
	default:
		return "", fmt.Errorf("tool state must be %q or %q", tool.ToolStatePrivate, tool.ToolStateHost)
	}
}

func ResolveStateRoot(value, home, base string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("stateRoot must not be empty")
	}
	value = expandHome(value, home)
	if filepath.IsAbs(value) {
		return value, nil
	}
	if base == "" {
		base = "."
	}
	return filepath.Join(base, value), nil
}

func expandHome(path, home string) string {
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return home + path[1:]
	}
	return path
}
