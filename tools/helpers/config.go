package helpers

// Config-file rendering helpers for tools that emit their own config: splitting
// command strings into argv, copying fields between generic maps, and joining
// instruction fragments.

import (
	"bytes"
	"fmt"

	"petris.dev/toby/config/file"
)

func CommandParts(name string, raw any) (string, []string, error) {
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
		args := make([]string, 0, len(command)-1)
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

func CopyField(dst, src map[string]any, from, to string) {
	if value, ok := src[from]; ok {
		dst[to] = configfile.Clone(value)
	}
}

func JoinInstructions(instructions [][]byte) []byte {
	parts := make([][]byte, 0, len(instructions))
	for _, item := range instructions {
		if len(bytes.TrimSpace(item)) == 0 {
			continue
		}
		parts = append(parts, bytes.TrimRight(item, "\n"))
	}
	if len(parts) == 0 {
		return nil
	}
	return append(bytes.Join(parts, []byte("\n\n")), '\n')
}

func JoinInstructionsString(instructions [][]byte) string {
	return string(JoinInstructions(instructions))
}

func JoinInstructionsOrNewline(instructions [][]byte) []byte {
	joined := JoinInstructions(instructions)
	if joined != nil {
		return joined
	}
	return []byte("\n")
}
