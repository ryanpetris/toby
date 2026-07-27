// Package exectool provides the exec tool, which runs a command in the Toby
// sandbox (defaulting to an interactive shell).
package exectool

import (
	"context"

	"petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/tools"
)

// Name is this tool's canonical identifier.
const Name = "exec"

// Meta is this tool's declarative identity, used both for planning (without
// construction) and by provide below.
var Meta = tools.Metadata{
	Name:          Name,
	DisplayName:   "Exec",
	LaunchHelp:    "Run a command in Toby Sandbox (default: interactive shell).",
	Group:         tools.GroupCommand,
	ContextGroups: []string{tools.GroupCommand, tools.GroupAI, tools.GroupUI, tools.GroupSystem, tools.GroupVCS},
}

// provide constructs the tool implementation.
func provide(sandbox sandbox.Service) result {
	svc := &execTool{
		Base:    tools.Base{Metadata: Meta},
		sandbox: sandbox,
	}
	return result{Service: svc}
}

type execTool struct {
	tools.Base
	sandbox sandbox.Service
}

var _ tools.Tool = (*execTool)(nil)

func (t *execTool) Launch(ctx context.Context, extra []string) error {
	_, err := t.sandbox.Exec(ctx, extra, sandbox.ExecOptions{Foreground: true})
	return err
}
