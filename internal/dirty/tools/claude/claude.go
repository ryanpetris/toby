package claude

import (
	"context"
	"path/filepath"
	"petris.dev/toby/container/layout"

	"petris.dev/toby/config"
	"petris.dev/toby/config/toby"
	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/control"
	"petris.dev/toby/control/httpproxy"
	"petris.dev/toby/internal/dirty/control/mcpproxy"
	claudeconfig "petris.dev/toby/internal/dirty/tools/claude/config"
	"petris.dev/toby/sandbox"
	"petris.dev/toby/tools"
	"petris.dev/toby/tools/helpers"
	"petris.dev/toby/tools/toolutil"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.claude", fx.Provide(Provide))

type Params struct {
	fx.In

	Paths        config.Paths
	Config       *tobyconfig.Service `optional:"true"`
	Proxy        *httpproxy.Service  `optional:"true"`
	MCPProxy     *mcpproxy.Service   `optional:"true"`
	Sandbox      sandbox.Service
	ContextFiles *contextfiles.Service
}

type Result struct {
	fx.Out

	Service tools.Tool `group:"toby.tools"`
}

func Provide(params Params) Result {
	svc := &claudeTool{
		Simple: toolutil.NewSimple(
			params.Paths,
			params.Sandbox,
			toolutil.DependentBase(tools.ClaudeToolName, "Launch Claude", 100, []string{tools.NpmToolName}, tools.GroupAI, tools.GroupSystem, tools.GroupVCS),
			[]string{".config", "claude"},
			[]string{".config", "claude"},
			[]string{"npm", "install", "-g", "@anthropic-ai/claude-code"},
			nil,
		),
		paths:        params.Paths,
		config:       params.Config,
		proxy:        params.Proxy,
		mcpProxy:     params.MCPProxy,
		contextFiles: params.ContextFiles,
	}
	return Result{Service: svc}
}

type claudeTool struct {
	*toolutil.Simple
	paths        config.Paths
	config       *tobyconfig.Service
	proxy        *httpproxy.Service
	mcpProxy     *mcpproxy.Service
	contextFiles *contextfiles.Service
	yolo         bool
}

func (t *claudeTool) PrepareHost(ctx context.Context, opts *tools.Options) error {
	if opts != nil {
		t.yolo = opts.YoloEnabled()
	}
	return t.Simple.PrepareHost(ctx, opts)
}

func (t *claudeTool) ConfigureSandbox(ctx context.Context) error {
	if err := t.Simple.ConfigureSandbox(ctx); err != nil {
		return err
	}
	return t.Sandbox.SetEnvironment(ctx, "CLAUDE_CONFIG_DIR", filepath.Join(layout.Home, ".config", "claude"))
}

func (t *claudeTool) InitSandbox(ctx context.Context) error {
	return t.Simple.InitSandbox(ctx)
}

func (t *claudeTool) RegisterContextFiles(ctx context.Context, opts tools.ContextOptions) error {
	return helpers.RegisterContextFilesOnce(ctx, t.Name(), func() error {
		controlHost, _ := t.Sandbox.GetEnvironment(control.EnvControlHost)
		return claudeconfig.RegisterContextFiles(t.contextFiles.Registrar(ctx), t.contextFiles.InstructionContents(), t.config, controlHost, t.Sandbox.TobyMCPURL(), t.proxy, t.mcpProxy)
	})
}

// Launch starts Claude Code, injecting Toby's generated context files through
// launch flags while Claude keeps its normal writable config directory.
func (t *claudeTool) Launch(ctx context.Context, extra []string) error {
	argv := append([]string{"claude"}, contextFlags(layout.Context)...)
	if t.yolo {
		argv = append(argv, "--dangerously-skip-permissions")
	}
	argv = append(argv, extra...)
	_, err := t.Sandbox.Exec(ctx, argv, sandbox.ExecOptions{Foreground: true})
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
