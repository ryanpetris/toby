package contextinit

// Embedded agent guidance compiled into the binary and written into the Toby
// instructions directory by the agent-instructions hook.

import (
	"embed"
	"io/fs"

	"petris.dev/toby/container/layout"
)

// TobyAgentsPath is the absolute destination the bundled agent guidance is written
// to; tobyAgentsFile is where the guidance lives in the embedded FS.
const (
	TobyAgentsPath = layout.Instructions + "/TOBY_AGENTS.md"
	tobyAgentsFile = "resources/TOBY_AGENTS.md"
)

//go:embed resources/TOBY_AGENTS.md
var agentFiles embed.FS

// AgentFiles returns the embedded agent guidance files.
func AgentFiles() fs.FS { return agentFiles }
