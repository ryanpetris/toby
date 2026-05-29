package claude

import (
	"context"
	"path/filepath"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/tool"
	"petris.dev/toby/internal/tools/toolutil"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.claude", fx.Provide(Provide))

type Params struct {
	fx.In

	Paths config.Paths
	NPM   tool.Tool `name:"npm"`
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
			map[string]string{"CLAUDE_CONFIG_DIR": filepath.Join(params.Paths.Home, ".config", "claude")},
		),
		paths: params.Paths,
		npm:   params.NPM,
	}
	return Result{Service: svc, Registry: svc}
}

type claudeTool struct {
	*tool.Simple
	paths config.Paths
	npm   tool.Tool
}

func (t *claudeTool) deps() []tool.Tool { return []tool.Tool{t.npm} }

func (t *claudeTool) Binds() []tool.Bind {
	return toolutil.Binds(t.deps(), t.Simple.Binds())
}

func (t *claudeTool) PathEntries() []string {
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
	return t.Simple.SandboxContextSetup(ctx)
}

func (t *claudeTool) SandboxInit(ctx context.Context, run *tool.RunContext) error {
	if err := toolutil.SandboxInitDependencies(ctx, run, t.npm); err != nil {
		return err
	}
	return t.Simple.SandboxInit(ctx, run)
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
// launch flags. CLAUDE_CONFIG_DIR stays the writable real config because Claude
// persists credentials and session state there.
func (t *claudeTool) Launch(ctx context.Context, run *tool.RunContext) error {
	argv := append([]string{"claude"}, contextFlags(t.paths.XDGRuntimeDir)...)
	argv = append(argv, run.Extra...)
	return tool.RunCommand(ctx, run.Launch, argv, tool.ExecOptions{})
}

func contextFlags(runtimeDir string) []string {
	base := filepath.Join(runtimeDir, "toby", "context", "claude")
	flags := []string{
		"--mcp-config", filepath.Join(base, "mcp.json"),
		"--settings", filepath.Join(base, "settings.json"),
		"--append-system-prompt-file", filepath.Join(base, "instructions.md"),
	}
	return flags
}
