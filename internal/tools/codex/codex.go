package codex

import (
	"context"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/tool"
	"petris.dev/toby/internal/tools/toolutil"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.codex", fx.Provide(Provide))

type Params struct {
	fx.In

	Paths config.Paths
	NPM   tool.Tool `name:"npm"`
}

type Result struct {
	fx.Out

	Service  tool.Tool `name:"codex"`
	Registry tool.Tool `group:"toby.tools"`
}

func Provide(params Params) Result {
	svc := &codexTool{
		Simple: toolutil.Simple(
			params.Paths,
			toolutil.Base(tool.CodexToolName, "Launch Codex", tool.GroupSystem, tool.GroupVCS),
			[]string{".config", "codex"},
			[]string{".codex"},
			[]string{"npm", "install", "-g", "@openai/codex"},
			nil,
		),
		npm: params.NPM,
	}
	return Result{Service: svc, Registry: svc}
}

type codexTool struct {
	*tool.Simple
	npm tool.Tool
}

func (t *codexTool) deps() []tool.Tool { return []tool.Tool{t.npm} }

func (t *codexTool) Binds() []tool.Bind {
	return toolutil.Binds(t.deps(), t.Simple.Binds())
}

func (t *codexTool) PathEntries() []tool.PathTarget {
	return toolutil.PathEntries(t.deps(), t.Simple.PathEntries())
}

func (t *codexTool) HostInit(ctx context.Context, opts *tool.CommandOptions) error {
	if err := toolutil.HostInitDependencies(ctx, opts, t.npm); err != nil {
		return err
	}
	return t.Simple.HostInit(ctx, opts)
}

func (t *codexTool) SandboxContextSetup(ctx *tool.RunContext) error {
	if err := toolutil.SandboxContextSetupDependencies(ctx, t.npm); err != nil {
		return err
	}
	return t.Simple.SandboxContextSetup(ctx)
}

func (t *codexTool) SandboxInit(ctx context.Context, run *tool.RunContext) error {
	if err := toolutil.SandboxInitDependencies(ctx, run, t.npm); err != nil {
		return err
	}
	return t.Simple.SandboxInit(ctx, run)
}

func (t *codexTool) Install(ctx context.Context, run *tool.RunContext) error {
	if err := toolutil.InstallDependencies(ctx, run, t.npm); err != nil {
		return err
	}
	return t.Simple.Install(ctx, run)
}

func (t *codexTool) Upgrade(ctx context.Context, run *tool.RunContext) error {
	if err := toolutil.UpgradeDependencies(ctx, run, t.npm); err != nil {
		return err
	}
	return t.Simple.Upgrade(ctx, run)
}
