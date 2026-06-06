// Package exectool provides the exec tool, which runs a command in the Toby
// sandbox (defaulting to an interactive shell).
package exectool

import (
	"context"

	"petris.dev/toby/sandbox"
	"petris.dev/toby/tools"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.exec", fx.Provide(Provide))

// Name is this tool's canonical identifier.
const Name = "exec"

// Meta is this tool's declarative identity, used both for planning (without
// construction) and by Provide below.
var Meta = tools.Metadata{
	Name:          Name,
	LaunchHelp:    "Run a command in Toby Sandbox (default: interactive shell).",
	Group:         tools.GroupCommand,
	ContextGroups: []string{tools.GroupCommand, tools.GroupAI, tools.GroupUI, tools.GroupSystem, tools.GroupVCS},
}

type Result struct {
	fx.Out

	Service tools.Tool `group:"tools"`
}

func Provide(sandbox sandbox.Service) Result {
	svc := &execTool{
		Base:    tools.Base{Metadata: Meta},
		sandbox: sandbox,
	}
	return Result{Service: svc}
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
