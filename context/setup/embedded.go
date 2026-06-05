package contextinit

// Embedded agent guidance compiled into the binary and written into the sandbox
// context directory by the agent-instructions hook.

import (
	"embed"
	"io/fs"
)

// TobyAgentsPath is the sandbox-relative name of the embedded Toby sandbox
// guidance instruction file.
const TobyAgentsPath = "TOBY_AGENTS.md"

//go:embed TOBY_AGENTS.md
var agentFiles embed.FS

// AgentFiles returns the embedded agent guidance files.
func AgentFiles() fs.FS { return agentFiles }
