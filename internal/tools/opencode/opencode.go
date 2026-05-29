package opencode

import (
	"context"
	"os"
	"path/filepath"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/tool"
	"petris.dev/toby/internal/tools/toolutil"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.opencode", fx.Provide(Provide))

type Params struct {
	fx.In

	Paths config.Paths
	NPM   tool.Tool `name:"npm"`
}

type Result struct {
	fx.Out

	Service  tool.Tool `name:"opencode"`
	Registry tool.Tool `group:"toby.tools"`
}

func Provide(params Params) Result {
	svc := &openCodeTool{
		Base:  toolutil.Base(tool.OpenCodeToolName, "Launch OpenCode", tool.GroupAI, tool.GroupSystem, tool.GroupVCS),
		paths: params.Paths,
		npm:   params.NPM,
	}
	return Result{Service: svc, Registry: svc}
}

type openCodeTool struct {
	tool.Base
	paths config.Paths
	npm   tool.Tool
}

func (t *openCodeTool) deps() []tool.Tool { return []tool.Tool{t.npm} }

func (t *openCodeTool) PathEntries() []string {
	return toolutil.PathEntries(t.deps(), nil)
}

func (t *openCodeTool) HostInit(ctx context.Context, opts *tool.CommandOptions) error {
	if err := toolutil.HostInitDependencies(ctx, opts, t.npm); err != nil {
		return err
	}
	return tool.HostInitOnce(opts, t.Name(), func() error {
		if err := os.MkdirAll(filepath.Join(t.paths.SandboxRoot, ".config", "opencode"), 0o755); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(t.paths.SandboxRoot, ".config", "opencode-share"), 0o755); err != nil {
			return err
		}
		return nil
	})
}

func (t *openCodeTool) Binds() []tool.Bind {
	own := []tool.Bind{
		{HostPath: filepath.Join(t.paths.SandboxRoot, ".config", "opencode"), SandboxPath: filepath.Join(t.paths.Home, ".config", "opencode"), Type: tool.BindRegular},
		{HostPath: filepath.Join(t.paths.SandboxRoot, ".config", "opencode-share"), SandboxPath: filepath.Join(t.paths.Home, ".local", "share", "opencode"), Type: tool.BindRegular},
	}
	return toolutil.Binds(t.deps(), own)
}

func (t *openCodeTool) SandboxContextSetup(ctx *tool.RunContext) error {
	if err := toolutil.SandboxContextSetupDependencies(ctx, t.npm); err != nil {
		return err
	}
	return tool.SandboxContextSetupOnce(ctx, t.Name(), func() error {
		ctx.Env["OPENCODE_CONFIG_DIR"] = filepath.Join(t.paths.XDGRuntimeDir, "toby", "context", "opencode")
		return nil
	})
}

func (t *openCodeTool) SandboxInit(ctx context.Context, run *tool.RunContext) error {
	return toolutil.SandboxInitDependencies(ctx, run, t.npm)
}

func (t *openCodeTool) Install(ctx context.Context, run *tool.RunContext) error {
	if err := toolutil.InstallDependencies(ctx, run, t.npm); err != nil {
		return err
	}
	return t.install(ctx, run, false)
}

func (t *openCodeTool) Upgrade(ctx context.Context, run *tool.RunContext) error {
	if err := toolutil.UpgradeDependencies(ctx, run, t.npm); err != nil {
		return err
	}
	return t.install(ctx, run, true)
}

func (t *openCodeTool) install(ctx context.Context, run *tool.RunContext, force bool) error {
	once := tool.InstallOnce
	if force {
		once = tool.UpgradeOnce
	}
	return once(run, t.Name(), func() error {
		if !force {
			exists, err := tool.CommandExists(ctx, run, "opencode")
			if err != nil || exists {
				return err
			}
		}
		return tool.RunCommand(ctx, run.Exec, []string{"npm", "install", "-g", "opencode-ai"}, tool.ExecOptions{})
	})
}

func (t *openCodeTool) Launch(ctx context.Context, run *tool.RunContext) error {
	return tool.RunCommand(ctx, run.Launch, append([]string{"opencode"}, run.Extra...), tool.ExecOptions{})
}
