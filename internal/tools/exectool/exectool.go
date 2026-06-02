package exectool

import (
	"context"

	"petris.dev/toby/internal/tools/tool"
	"petris.dev/toby/internal/tools/toolutil"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.exec", fx.Provide(Provide))

type Result struct {
	fx.Out

	Service tool.Tool `group:"toby.tools"`
}

func Provide(sandbox tool.SandboxService) Result {
	svc := &execTool{
		Base: toolutil.Base(
			tool.ExecToolName,
			"Run a command in Toby Sandbox (default: interactive shell).",
			tool.GroupAI,
			tool.GroupUI,
			tool.GroupSystem,
			tool.GroupVCS,
		),
		sandbox: sandbox,
	}
	return Result{Service: svc}
}

type execTool struct {
	tool.Base
	sandbox tool.SandboxService
}

func (t *execTool) Launch(ctx context.Context, extra []string) error {
	_, err := t.sandbox.Exec(ctx, extra, tool.ExecOptions{Foreground: true})
	return err
}
