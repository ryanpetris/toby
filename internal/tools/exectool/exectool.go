package exectool

import (
	"context"

	"petris.dev/toby/internal/tool"
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
	return tool.RunCommand(ctx, run.Launch, commandOrShell(run.Extra, run.Env), tool.ExecOptions{})
}

func commandOrShell(extra []string, env tool.Environment) []string {
	if len(extra) > 0 {
		return extra
	}
	shell := env["SHELL"]
	if shell == "" {
		shell = "/bin/sh"
	}
	return []string{shell, "-i"}
}
