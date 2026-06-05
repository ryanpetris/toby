// Package exectool provides the exec tool, which runs a command in the Toby
// sandbox (defaulting to an interactive shell).
package exectool

import (
	"context"

	"petris.dev/toby/sandbox"
	"petris.dev/toby/tools"
	"petris.dev/toby/tools/kit"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.exec", fx.Provide(Provide))

type Result struct {
	fx.Out

	Service tools.Tool `group:"tools"`
}

func Provide(sandbox sandbox.Service) Result {
	svc := &execTool{
		Base: kit.Base(
			tools.ExecToolName,
			"Run a command in Toby Sandbox (default: interactive shell).",
			tools.GroupCommand,
			tools.GroupAI,
			tools.GroupUI,
			tools.GroupSystem,
			tools.GroupVCS,
		),
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
