package t3

import (
	"context"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/tool"
	"petris.dev/toby/internal/tools/toolutil"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.t3", fx.Provide(Provide))

type Params struct {
	fx.In

	Paths config.Paths
	NPM   tool.Tool `name:"npm"`
}

type Result struct {
	fx.Out

	Service  tool.Tool `name:"t3"`
	Registry tool.Tool `group:"toby.tools"`
}

func Provide(params Params) Result {
	svc := &t3Tool{
		Simple: toolutil.Simple(
			params.Paths,
			toolutil.Base(tool.T3ToolName, "Launch T3 Code", tool.GroupUI, tool.GroupAI, tool.GroupSystem, tool.GroupVCS),
			nil,
			nil,
			[]string{"npm", "install", "-g", "t3"},
			map[string]string{"T3CODE_NO_BROWSER": "1"},
		),
		npm: params.NPM,
	}
	return Result{Service: svc, Registry: svc}
}

type t3Tool struct {
	*tool.Simple
	npm tool.Tool
}

func (t *t3Tool) deps() []tool.Tool { return []tool.Tool{t.npm} }

func (t *t3Tool) Binds() []tool.Bind {
	return toolutil.Binds(t.deps(), t.Simple.Binds())
}

func (t *t3Tool) PathEntries() []string {
	return toolutil.PathEntries(t.deps(), t.Simple.PathEntries())
}

func (t *t3Tool) HostInit(ctx context.Context, opts *tool.CommandOptions) error {
	if err := toolutil.HostInitDependencies(ctx, opts, t.npm); err != nil {
		return err
	}
	return t.Simple.HostInit(ctx, opts)
}

func (t *t3Tool) SandboxContextSetup(ctx *tool.RunContext) error {
	if err := toolutil.SandboxContextSetupDependencies(ctx, t.npm); err != nil {
		return err
	}
	return t.Simple.SandboxContextSetup(ctx)
}

func (t *t3Tool) SandboxInit(ctx context.Context, run *tool.RunContext) error {
	if err := toolutil.SandboxInitDependencies(ctx, run, t.npm); err != nil {
		return err
	}
	return t.Simple.SandboxInit(ctx, run)
}

func (t *t3Tool) Install(ctx context.Context, run *tool.RunContext) error {
	if err := toolutil.InstallDependencies(ctx, run, t.npm); err != nil {
		return err
	}
	return t.Simple.Install(ctx, run)
}

func (t *t3Tool) Upgrade(ctx context.Context, run *tool.RunContext) error {
	if err := toolutil.UpgradeDependencies(ctx, run, t.npm); err != nil {
		return err
	}
	return t.Simple.Upgrade(ctx, run)
}
