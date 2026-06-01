package claude

import (
	"context"
	"path/filepath"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/config/toby"
	contextfiles "petris.dev/toby/internal/context/files"
	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/control/httpproxy"
	claudeconfig "petris.dev/toby/internal/tools/claude/config"
	"petris.dev/toby/internal/tools/helpers"
	"petris.dev/toby/internal/tools/tool"
	"petris.dev/toby/internal/tools/toolutil"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.claude", fx.Provide(Provide))

type Params struct {
	fx.In

	Paths        config.Paths
	NPM          tool.Tool           `name:"npm"`
	Config       *tobyconfig.Service `optional:"true"`
	Proxy        *httpproxy.Service  `optional:"true"`
	Sandbox      tool.SandboxService
	ContextFiles *contextfiles.Service
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
			params.Sandbox,
			toolutil.Base(tool.ClaudeToolName, "Launch Claude", tool.GroupSystem, tool.GroupVCS),
			[]string{".config", "claude"},
			[]string{".config", "claude"},
			[]string{"npm", "install", "-g", "@anthropic-ai/claude-code"},
			nil,
		),
		paths:        params.Paths,
		npm:          params.NPM,
		config:       params.Config,
		proxy:        params.Proxy,
		contextFiles: params.ContextFiles,
	}
	return Result{Service: svc, Registry: svc}
}

type claudeTool struct {
	*tool.Simple
	paths        config.Paths
	npm          tool.Tool
	config       *tobyconfig.Service
	proxy        *httpproxy.Service
	contextFiles *contextfiles.Service
}

func (t *claudeTool) deps() []tool.Tool { return []tool.Tool{t.npm} }

func (t *claudeTool) HostInit(ctx context.Context, opts *tool.CommandOptions) error {
	if err := toolutil.HostInitDependencies(ctx, opts, t.npm); err != nil {
		return err
	}
	return t.Simple.HostInit(ctx, opts)
}

func (t *claudeTool) SandboxContextSetup(ctx context.Context) error {
	if err := toolutil.SandboxContextSetupDependencies(ctx, t.npm); err != nil {
		return err
	}
	if err := t.Simple.SandboxContextSetup(ctx); err != nil {
		return err
	}
	return t.Sandbox.SetEnvironment(ctx, "CLAUDE_CONFIG_DIR", filepath.Join(t.Sandbox.Paths().Home, ".config", "claude"))
}

func (t *claudeTool) SandboxInit(ctx context.Context) error {
	if err := toolutil.SandboxInitDependencies(ctx, t.npm); err != nil {
		return err
	}
	return t.Simple.SandboxInit(ctx)
}

func (t *claudeTool) RegisterContextFiles(ctx context.Context, opts tool.ContextOptions) error {
	return helpers.RegisterContextFilesOnce(ctx, t.Name(), func() error {
		if registrar, ok := t.npm.(tool.ContextFileTool); ok {
			if err := registrar.RegisterContextFiles(ctx, opts); err != nil {
				return err
			}
		}
		controlHost, _ := t.Sandbox.GetEnvironment(control.EnvControlHost)
		return claudeconfig.RegisterContextFiles(t.contextFiles.Registrar(ctx), t.Sandbox.Paths().Workspace, t.contextFiles.InstructionContents(), t.config, controlHost, t.Sandbox.TobyMCPURL(), t.proxy)
	})
}

func (t *claudeTool) Install(ctx context.Context) error {
	if err := toolutil.InstallDependencies(ctx, t.npm); err != nil {
		return err
	}
	return t.Simple.Install(ctx)
}

func (t *claudeTool) Upgrade(ctx context.Context) error {
	if err := toolutil.UpgradeDependencies(ctx, t.npm); err != nil {
		return err
	}
	return t.Simple.Upgrade(ctx)
}

// Launch starts Claude Code, injecting Toby's generated context files through
// launch flags while Claude keeps its normal writable config directory.
func (t *claudeTool) Launch(ctx context.Context, extra []string) error {
	argv := append([]string{"claude"}, contextFlags(t.Sandbox.Paths().Context)...)
	argv = append(argv, extra...)
	_, err := t.Sandbox.Exec(ctx, argv, tool.ExecOptions{Foreground: true})
	return err
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
