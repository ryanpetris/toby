package helpers

// Config-file rendering helpers for tools that emit their own config: joining
// instruction fragments.

import "bytes"

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
