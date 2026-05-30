package opencode

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/opencodeconfig"
	"petris.dev/toby/internal/tobyconfig"
	"petris.dev/toby/internal/tool"
	"petris.dev/toby/internal/tools/toolutil"
	"petris.dev/toby/internal/warning"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.opencode", fx.Provide(Provide))

type Params struct {
	fx.In

	Paths    config.Paths
	NPM      tool.Tool                `name:"npm"`
	Renderer *opencodeconfig.Renderer `optional:"true"`
	Config   *tobyconfig.Service      `optional:"true"`
}

type Result struct {
	fx.Out

	Service  tool.Tool `name:"opencode"`
	Registry tool.Tool `group:"toby.tools"`
}

func Provide(params Params) Result {
	svc := &openCodeTool{
		Base:     toolutil.Base(tool.OpenCodeToolName, "Launch OpenCode", tool.GroupAI, tool.GroupSystem, tool.GroupVCS),
		paths:    params.Paths,
		npm:      params.NPM,
		renderer: params.Renderer,
		config:   params.Config,
	}
	return Result{Service: svc, Registry: svc}
}

type openCodeTool struct {
	tool.Base
	paths    config.Paths
	npm      tool.Tool
	renderer *opencodeconfig.Renderer
	config   *tobyconfig.Service
}

func (t *openCodeTool) deps() []tool.Tool { return []tool.Tool{t.npm} }

func (t *openCodeTool) PathEntries() []tool.PathTarget {
	return toolutil.PathEntries(t.deps(), nil)
}

func (t *openCodeTool) HostInit(ctx context.Context, opts *tool.CommandOptions) error {
	if err := toolutil.HostInitDependencies(ctx, opts, t.npm); err != nil {
		return err
	}
	if opts.ToolStateFor(t.Name()) != tool.ToolStateHost {
		return nil
	}
	return tool.HostInitOnce(opts, t.Name(), func() error {
		for _, dir := range t.stateDirs(opts.ToolStateRootFor(t.Name())) {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
		}
		return nil
	})
}

func (t *openCodeTool) Binds() []tool.Bind {
	own := []tool.Bind{
		{HostPath: filepath.Join(t.paths.SandboxRoot, ".config", "opencode"), Target: tool.HomeTarget(".config", "opencode"), Type: tool.BindRegular, State: true, StatePath: filepath.ToSlash(filepath.Join(".config", "opencode"))},
		{HostPath: filepath.Join(t.paths.SandboxRoot, ".local", "share", "opencode"), Target: tool.HomeTarget(".local", "share", "opencode"), Type: tool.BindRegular, State: true, StatePath: filepath.ToSlash(filepath.Join(".local", "share", "opencode"))},
	}
	return toolutil.Binds(t.deps(), own)
}

func (t *openCodeTool) stateDirs(root string) []string {
	return []string{
		filepath.Join(root, ".config", "opencode"),
		filepath.Join(root, ".local", "share", "opencode"),
	}
}

func (t *openCodeTool) SandboxContextSetup(ctx *tool.RunContext) error {
	if err := toolutil.SandboxContextSetupDependencies(ctx, t.npm); err != nil {
		return err
	}
	return tool.SandboxContextSetupOnce(ctx, t.Name(), func() error {
		ctx.Env["OPENCODE_CONFIG_DIR"] = ctx.Sandbox.TobyOpenCodeConfigDir()
		return nil
	})
}

func (t *openCodeTool) SandboxInit(ctx context.Context, run *tool.RunContext) error {
	return toolutil.SandboxInitDependencies(ctx, run, t.npm)
}

func (t *openCodeTool) RegisterContextFiles(ctx context.Context, run *tool.RunContext) error {
	if t.renderer == nil {
		return fmt.Errorf("opencode renderer is not configured")
	}
	if run == nil || run.ContextFiles == nil {
		return fmt.Errorf("context files session is not configured")
	}
	warnings, err := t.renderer.RegisterContextFiles(ctx, run.ContextFiles, run.Sandbox.Projects(), run.ContextFiles.InstructionPaths(), t.config)
	if err != nil {
		return err
	}
	var suppression warning.Suppression
	if run.Options != nil {
		suppression = run.Options.SuppressWarnings
	}
	for _, item := range warnings {
		warning.Fprintf(run.Stderr, suppression, warning.OpenCodeModelDiscovery, "failed to fetch OpenCode models: %v", item)
	}
	return nil
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
