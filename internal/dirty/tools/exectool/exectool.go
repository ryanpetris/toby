package exectool

import (
	"context"

	"petris.dev/toby/internal/dirty/tools/toolutil"
	"petris.dev/toby/sandbox"
	"petris.dev/toby/tools"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.exec", fx.Provide(Provide))

type Result struct {
	fx.Out

	Service tools.Tool `group:"toby.tools"`
}

func Provide(sandbox sandbox.Service) Result {
	svc := &execTool{
		Base: toolutil.Base(
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

func (t *execTool) Launch(ctx context.Context, extra []string) error {
	_, err := t.sandbox.Exec(ctx, extra, sandbox.ExecOptions{Foreground: true})
	return err
}
