package claude

import (
	"context"
	"fmt"
	"path/filepath"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/config/toby"
	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/control/httpproxy"
	claudeconfig "petris.dev/toby/internal/tools/claude/config"
	"petris.dev/toby/internal/tools/tool"
	"petris.dev/toby/internal/tools/toolutil"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.claude", fx.Provide(Provide))

type Params struct {
	fx.In

	Paths  config.Paths
	NPM    tool.Tool           `name:"npm"`
	Config *tobyconfig.Service `optional:"true"`
	Proxy  *httpproxy.Service  `optional:"true"`
}

type Result struct {
	fx.Out

	Service  tool.Tool `name:"claude"`
	Registry tool.Tool `group:"toby.tools"`
}

func Provide(params Params) Result {
	svc := &claudeTool{
		Simple: toolutil.Simple(
			params.Paths,
			toolutil.Base(tool.ClaudeToolName, "Launch Claude", tool.GroupSystem, tool.GroupVCS),
			[]string{".config", "claude"},
			[]string{".config", "claude"},
			[]string{"npm", "install", "-g", "@anthropic-ai/claude-code"},
			nil,
		),
		paths:  params.Paths,
		npm:    params.NPM,
		config: params.Config,
		proxy:  params.Proxy,
	}
	return Result{Service: svc, Registry: svc}
}

type claudeTool struct {
	*tool.Simple
	paths  config.Paths
	npm    tool.Tool
	config *tobyconfig.Service
	proxy  *httpproxy.Service
}

func (t *claudeTool) deps() []tool.Tool { return []tool.Tool{t.npm} }

func (t *claudeTool) Binds() []tool.Bind {
	return toolutil.Binds(t.deps(), t.Simple.Binds())
}

func (t *claudeTool) PathEntries() []tool.PathTarget {
	return toolutil.PathEntries(t.deps(), t.Simple.PathEntries())
}

func (t *claudeTool) HostInit(ctx context.Context, opts *tool.CommandOptions) error {
	if err := toolutil.HostInitDependencies(ctx, opts, t.npm); err != nil {
		return err
	}
	return t.Simple.HostInit(ctx, opts)
}

func (t *claudeTool) SandboxContextSetup(ctx *tool.RunContext) error {
	if err := toolutil.SandboxContextSetupDependencies(ctx, t.npm); err != nil {
		return err
	}
	if err := t.Simple.SandboxContextSetup(ctx); err != nil {
		return err
	}
	ctx.Env["CLAUDE_CONFIG_DIR"] = filepath.Join(ctx.Sandbox.HomeDir(), ".config", "claude")
	return nil
}

func (t *claudeTool) SandboxInit(ctx context.Context, run *tool.RunContext) error {
	if err := toolutil.SandboxInitDependencies(ctx, run, t.npm); err != nil {
		return err
	}
	return t.Simple.SandboxInit(ctx, run)
}

func (t *claudeTool) RegisterContextFiles(ctx context.Context, run *tool.RunContext) error {
	return tool.RegisterContextFilesOnce(run, t.Name(), func() error {
		if run == nil || run.ContextFiles == nil {
			return fmt.Errorf("context files session is not configured")
		}
		if registrar, ok := t.npm.(tool.ContextFileTool); ok {
			if err := registrar.RegisterContextFiles(ctx, run); err != nil {
				return err
			}
		}
		return claudeconfig.RegisterContextFiles(run.ContextFiles, run.Sandbox.Projects(), run.ContextFiles.InstructionContents(), t.config, run.Env[control.EnvControlHost], run.TobyMCPURL, t.proxy)
	})
}

func (t *claudeTool) Install(ctx context.Context, run *tool.RunContext) error {
	if err := toolutil.InstallDependencies(ctx, run, t.npm); err != nil {
		return err
	}
	return t.Simple.Install(ctx, run)
}

func (t *claudeTool) Upgrade(ctx context.Context, run *tool.RunContext) error {
	if err := toolutil.UpgradeDependencies(ctx, run, t.npm); err != nil {
		return err
	}
	return t.Simple.Upgrade(ctx, run)
}

// Launch starts Claude Code, injecting Toby's generated context files through
// launch flags while Claude keeps its normal writable config directory.
func (t *claudeTool) Launch(ctx context.Context, run *tool.RunContext) error {
	argv := append([]string{"claude"}, contextFlags(run.Sandbox.TobyContextDir())...)
	argv = append(argv, run.Extra...)
	return tool.RunCommand(ctx, run.Launch, argv, tool.ExecOptions{})
}

func contextFlags(contextDir string) []string {
	base := filepath.Join(contextDir, "claude")
	flags := []string{
		"--mcp-config", filepath.Join(base, "mcp.json"),
		"--settings", filepath.Join(base, "settings.json"),
		"--append-system-prompt-file", filepath.Join(base, "instructions.md"),
	}
	return flags
}
