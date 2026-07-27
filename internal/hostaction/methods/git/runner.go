package git

// Defines the Linux process-supervision boundary used to run host-authority
// Git commands without reopening their authorized repository directory.

import (
	"context"
	"os"
)

// CommandRunner executes one command with directory as its authoritative
// working-directory capability and captures output through direct files.
type CommandRunner interface {
	// RunHostCommand executes a Git command using an opened directory capability.
	RunHostCommand(
		context.Context,
		*os.File,
		[]string,
		*os.File,
		*os.File,
		*os.File,
	) (int, error)
}
