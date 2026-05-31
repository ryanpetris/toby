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

	Service  tool.Tool `name:"exec"`
	Registry tool.Tool `group:"toby.tools"`
}

func Provide() Result {
	svc := &execTool{
		Base: toolutil.Base(
			tool.ExecToolName,
			"Run a command in Toby Sandbox (default: interactive shell).",
			tool.GroupAI,
			tool.GroupUI,
			tool.GroupSystem,
			tool.GroupVCS,
		),
	}
	return Result{Service: svc, Registry: svc}
}

type execTool struct{ tool.Base }

func (t *execTool) Launch(ctx context.Context, run *tool.RunContext) error {
	return tool.RunCommand(ctx, run.Launch, run.Extra, tool.ExecOptions{})
}
