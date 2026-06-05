// Package embedded holds context files compiled into the binary.
package embedded

import (
	"embed"
	"io/fs"
)

// TobyAgentsPath is the sandbox-relative name of the embedded Toby sandbox
// guidance instruction file.
const TobyAgentsPath = "TOBY_AGENTS.md"

//go:embed TOBY_AGENTS.md
var files embed.FS

// AgentFiles returns the embedded agent guidance files.
func AgentFiles() fs.FS { return files }
