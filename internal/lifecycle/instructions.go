package lifecycle

// Embeds the Toby sandbox instructions appended to every native agent launch.

import _ "embed"

//go:embed resources/TOBY_AGENTS.md
var sandboxInstructions []byte

// SandboxInstructions returns a detached copy of Toby's native sandbox
// instructions for inclusion in run-scoped agent configuration.
func SandboxInstructions() []byte {
	return append([]byte(nil), sandboxInstructions...)
}
