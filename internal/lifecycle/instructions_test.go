package lifecycle

// Verifies the embedded native sandbox instructions remain available.

import (
	"bytes"
	"testing"
)

func TestSandboxInstructions(t *testing.T) {
	instructions := SandboxInstructions()
	if !bytes.Contains(instructions, []byte("# Toby Sandbox")) ||
		!bytes.Contains(instructions, []byte("git.commit")) {
		t.Fatalf("sandbox instructions are incomplete: %q", instructions)
	}

	instructions[0] = '!'
	if bytes.Equal(instructions, SandboxInstructions()) {
		t.Fatal("SandboxInstructions returned shared mutable storage")
	}
}
