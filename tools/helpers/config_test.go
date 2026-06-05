package helpers_test

import (
	"testing"

	"petris.dev/toby/tools/helpers"
)

func TestJoinInstructions(t *testing.T) {
	instructions := [][]byte{[]byte("\n"), []byte("# one\n"), []byte("# two\n\n")}
	if got := string(helpers.JoinInstructions(instructions)); got != "# one\n\n# two\n" {
		t.Fatalf("joined = %q", got)
	}
	if got := helpers.JoinInstructionsString([][]byte{[]byte("  ")}); got != "" {
		t.Fatalf("empty string join = %q", got)
	}
	if got := string(helpers.JoinInstructionsOrNewline(nil)); got != "\n" {
		t.Fatalf("empty newline join = %q", got)
	}
}
